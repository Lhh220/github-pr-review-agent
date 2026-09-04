package review

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/liaohonghui/github-pr-review-agent/internal/github"
	"github.com/liaohonghui/github-pr-review-agent/internal/llm"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

type fakeGitHubClient struct {
	pr          *github.PullRequest
	files       []github.PullRequestFile
	fileContent string
	reviewBody  string
}

func (f *fakeGitHubClient) GetPullRequest(ctx context.Context, owner, repo string, number int) (*github.PullRequest, error) {
	return f.pr, nil
}

func (f *fakeGitHubClient) GetPullRequestFiles(ctx context.Context, owner, repo string, number int) ([]github.PullRequestFile, error) {
	return f.files, nil
}

func (f *fakeGitHubClient) GetFileContent(ctx context.Context, owner, repo, path, ref string) (string, error) {
	return f.fileContent, nil
}

func (f *fakeGitHubClient) CreatePullRequestReview(ctx context.Context, owner, repo string, number int, body string) error {
	f.reviewBody = body
	return nil
}

type fakeLLMClient struct {
	title       string
	body        string
	diff        string
	fileContext string
	response    llm.ReviewResponse
	called      bool
}

func (f *fakeLLMClient) ReviewCode(ctx context.Context, title, body, diff, fileContext string) (llm.ReviewResponse, error) {
	f.called = true
	f.title = title
	f.body = body
	f.diff = diff
	f.fileContext = fileContext
	return f.response, nil
}

type fakeResultStore struct {
	input store.NewReviewResult
	err   error
}

func (f *fakeResultStore) CreateReviewResult(ctx context.Context, input store.NewReviewResult) (*store.ReviewResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.input = input
	return &store.ReviewResult{
		ID:            1,
		TaskID:        input.TaskID,
		Summary:       input.Summary,
		Findings:      input.Findings,
		RawResponse:   input.RawResponse,
		Model:         input.Model,
		InputTokens:   input.InputTokens,
		OutputTokens:  input.OutputTokens,
		TotalTokens:   input.TotalTokens,
		LLMDurationMS: input.LLMDurationMS,
		CreatedAt:     time.Now(),
	}, nil
}

func TestReviewPRSkipsLLMWhenNoChangedFiles(t *testing.T) {
	gh := &fakeGitHubClient{
		pr: &github.PullRequest{
			Title: "No-op",
			Body:  "No changes.",
			Head:  github.Ref{SHA: "291ac5aedc5fd96c5030a6c18e91923140677591"},
		},
		files: []github.PullRequestFile{},
	}
	fakeLLM := &fakeLLMClient{response: llm.ReviewResponse{Content: "should not be called"}}
	results := &fakeResultStore{}
	service := New(gh, fakeLLM, results, 100, 10, 100)

	if err := service.ReviewPR(context.Background(), "owner", "repo", 12, 1); err != nil {
		t.Fatalf("ReviewPR() error = %v", err)
	}
	if fakeLLM.called {
		t.Fatal("LLM was called even though the pull request had no changed files")
	}
	if results.input.Model != "none" || len(results.input.Findings) != 0 {
		t.Fatalf("unexpected no-diff result input: %+v", results.input)
	}
	if !strings.Contains(gh.reviewBody, "no changed files relative to its base branch") ||
		!strings.Contains(gh.reviewBody, "Task ID: 1 | commit 291ac5a") {
		t.Fatalf("unexpected no-diff review comment: %s", gh.reviewBody)
	}
}

func TestReviewPR(t *testing.T) {
	gh := &fakeGitHubClient{
		pr: &github.PullRequest{
			Title: "Fix login bug",
			Body:  "Fixes token validation.",
			Head:  github.Ref{SHA: "291ac5aedc5fd96c5030a6c18e91923140677591"},
		},
		files: []github.PullRequestFile{
			{Filename: "internal/auth/auth.go", Patch: "@@ -1 +1 @@\n-fix\n+fix token"},
		},
		fileContent: "func ValidateToken() {}",
	}
	fakeLLM := &fakeLLMClient{response: llm.ReviewResponse{
		Content: `{"summary":"No blocking issues.","findings":[]}`,
		Model:   "deepseek-chat",
		Usage: llm.Usage{
			InputTokens:  100,
			OutputTokens: 20,
			TotalTokens:  120,
		},
		DurationMS: 1234,
	}}
	results := &fakeResultStore{}
	service := New(gh, fakeLLM, results, 100, 10, 100)

	if err := service.ReviewPR(context.Background(), "owner", "repo", 12, 1); err != nil {
		t.Fatalf("ReviewPR() error = %v", err)
	}
	if fakeLLM.title != "Fix login bug" || fakeLLM.body != "Fixes token validation." {
		t.Fatalf("unexpected PR metadata sent to LLM: %+v", fakeLLM)
	}
	if !strings.Contains(fakeLLM.diff, "internal/auth/auth.go") || !strings.Contains(fakeLLM.fileContext, "func ValidateToken") {
		t.Fatalf("unexpected model context: diff=%q fileContext=%q", fakeLLM.diff, fakeLLM.fileContext)
	}
	if results.input.Summary != "No blocking issues." || results.input.Model != "deepseek-chat" ||
		results.input.InputTokens != 100 || results.input.OutputTokens != 20 ||
		results.input.TotalTokens != 120 || results.input.LLMDurationMS != 1234 {
		t.Fatalf("unexpected result input: %+v", results.input)
	}
	if !strings.Contains(gh.reviewBody, "No blocking issues.") ||
		!strings.Contains(gh.reviewBody, "Task ID: 1 | commit 291ac5a") {
		t.Fatalf("unexpected review comment: %s", gh.reviewBody)
	}
}
