package review

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/liaohonghui/github-pr-review-agent/internal/github"
	"github.com/liaohonghui/github-pr-review-agent/internal/llm"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

type deliveryTestStore struct {
	fakeAgentStore
	creates, markFailures int
	loseResultAck         bool
}

func (s *deliveryTestStore) CreateReviewResult(ctx context.Context, in store.NewReviewResult) (*store.ReviewResult, error) {
	s.creates++
	if s.result.TaskID != 0 {
		return nil, errors.New("duplicate result")
	}
	r, err := s.fakeAgentStore.CreateReviewResult(ctx, in)
	if s.loseResultAck {
		s.loseResultAck = false
		return nil, errors.New("result commit response lost")
	}
	return r, err
}
func (s *deliveryTestStore) MarkReviewDelivered(ctx context.Context, taskID, id uint64) error {
	if s.markFailures > 0 {
		s.markFailures--
		return errors.New("database unavailable after GitHub success")
	}
	return s.fakeAgentStore.MarkReviewDelivered(ctx, taskID, id)
}

type deliveryTestGitHub struct {
	fakeAgentGitHubClient
	reviews                            []github.PullRequestReview
	posts, lists                       int
	failBefore, loseResponse, failList bool
}

func (g *deliveryTestGitHub) ListPullRequestReviews(context.Context, string, string, int) ([]github.PullRequestReview, error) {
	g.lists++
	if g.failList {
		g.failList = false
		return nil, errors.New("listing failed")
	}
	return g.reviews, nil
}
func (g *deliveryTestGitHub) CreatePullRequestReviewAtCommit(_ context.Context, _, _ string, _ int, sha, body string) (uint64, error) {
	g.posts++
	if g.failBefore {
		g.failBefore = false
		return 0, errors.New("request not delivered")
	}
	g.reviews = append(g.reviews, github.PullRequestReview{ID: 123, CommitID: sha, Body: body, State: "COMMENTED"})
	if g.loseResponse {
		g.loseResponse = false
		return 0, errors.New("response lost after server accepted review")
	}
	return 123, nil
}

type deliveryTestModel struct{ calls int }

func (m *deliveryTestModel) ReviewCode(context.Context, string, string, string, string) (llm.ReviewResponse, error) {
	m.calls++
	return llm.ReviewResponse{Content: `{"summary":"Reviewed","findings":[]}`}, nil
}
func (m *deliveryTestModel) ChatWithTools(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	m.calls++
	return llm.ChatResponse{Content: `{"summary":"Reviewed","findings":[]}`}, nil
}

func TestReviewDeliveryResumesWithoutDuplicateAnalysisOrComment(t *testing.T) {
	for _, mode := range []string{"legacy", "agent", "legacy-docs", "agent-docs"} {
		for _, fault := range []string{"post-not-sent", "response-lost", "mark-failed", "result-ack-lost", "list-failed"} {
			t.Run(mode+"/"+fault, func(t *testing.T) {
				ctx := context.Background()
				g := &deliveryTestGitHub{fakeAgentGitHubClient: fakeAgentGitHubClient{pr: &github.PullRequest{Head: github.Ref{SHA: "head-one"}}, files: []github.PullRequestFile{{Filename: "a.go", Patch: "@@ -0,0 +1 @@\n+package main"}}}}
				if strings.HasSuffix(mode, "docs") {
					g.files[0].Filename = "README.md"
				}
				s := &deliveryTestStore{}
				switch fault {
				case "post-not-sent":
					g.failBefore = true
				case "response-lost":
					g.loseResponse = true
				case "mark-failed":
					s.markFailures = 1
				case "result-ack-lost":
					s.loseResultAck = true
				case "list-failed":
					g.failList = true
				}
				m := &deliveryTestModel{}
				var run func(context.Context, string, string, int, uint64) error
				if strings.HasPrefix(mode, "legacy") {
					run = New(g, m, s, 100, 0, 100).ReviewPR
				} else {
					run = NewAgent(g, m, s, AgentOptions{}).ReviewPR
				}
				if err := run(ctx, "owner", "repo", 1, 7); err == nil {
					t.Fatal("injected failure was lost")
				}
				// A newer PR head must not relabel an already persisted result during retry.
				g.pr.Head.SHA = "head-two"
				if err := run(ctx, "owner", "repo", 1, 7); err != nil {
					t.Fatal(err)
				}
				if err := run(ctx, "owner", "repo", 1, 7); err != nil {
					t.Fatal(err)
				}
				wantCalls := 1
				if strings.HasSuffix(mode, "docs") {
					wantCalls = 0
				}
				if m.calls != wantCalls || s.creates != 1 || len(g.reviews) != 1 || s.delivery.GitHubReviewID != 123 {
					t.Fatalf("calls=%d creates=%d reviews=%+v delivery=%+v", m.calls, s.creates, g.reviews, s.delivery)
				}
				if g.reviews[0].CommitID != "head-one" || !strings.Contains(g.reviews[0].Body, "<!-- pr-review-agent:") {
					t.Fatalf("missing pinned commit or marker: %+v", g.reviews)
				}
				wantPosts := 1
				if fault == "post-not-sent" {
					wantPosts = 2
				}
				if g.posts != wantPosts {
					t.Fatalf("POST count=%d want=%d", g.posts, wantPosts)
				}
			})
		}
	}
}

func TestDeliveryRefusesLegacyResultsAndChangedSnapshot(t *testing.T) {
	ctx := context.Background()
	g := &deliveryTestGitHub{fakeAgentGitHubClient: fakeAgentGitHubClient{pr: &github.PullRequest{Head: github.Ref{SHA: "new-head"}}}}
	s := &deliveryTestStore{}
	s.result = store.NewReviewResult{TaskID: 1, Summary: "old result"}
	if _, _, err := resumeReview(ctx, g, s, "o", "r", 1, 1); err == nil {
		t.Fatal("legacy result silently republished")
	}
	s.result = store.NewReviewResult{}
	s.delivery = &store.ReviewDelivery{TaskID: 1, CommitSHA: "old-head", Marker: "marker"}
	if _, _, err := resumeReview(ctx, g, s, "o", "r", 1, 1); err == nil {
		t.Fatal("snapshot changed on retry")
	}
	if err := finishReview(ctx, g, s, "o", "r", 1, store.NewReviewResult{TaskID: 1}); err == nil {
		t.Fatal("head changed during analysis")
	}
	if g.posts != 0 || s.creates != 0 {
		t.Fatal("published or persisted mixed version")
	}
}

func TestPublishMatchesOnlySubmittedSnapshot(t *testing.T) {
	s := &deliveryTestStore{}
	s.delivery = &store.ReviewDelivery{TaskID: 7, CommitSHA: "target", Marker: "marker"}
	g := &deliveryTestGitHub{reviews: []github.PullRequestReview{
		{ID: 1, CommitID: "other", State: "COMMENTED", Body: "<!-- pr-review-agent:marker -->"},
		{ID: 2, CommitID: "target", State: "PENDING", Body: "<!-- pr-review-agent:marker -->"},
		{ID: 3, CommitID: "target", State: "COMMENTED", Body: "<!-- pr-review-agent:marker-other -->"},
		{ID: 4, CommitID: "target", State: "COMMENTED", Body: "<!-- pr-review-agent:marker -->"},
	}}
	if err := publishSavedReview(context.Background(), g, s, "o", "r", 1, &store.ReviewResult{TaskID: 7}, s.delivery); err != nil {
		t.Fatal(err)
	}
	if s.delivery.GitHubReviewID != 4 || g.posts != 0 {
		t.Fatalf("delivery=%+v posts=%d", s.delivery, g.posts)
	}
}
