package review

import (
	"context"
	"strings"
	"testing"

	"github.com/liaohonghui/github-pr-review-agent/internal/github"
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
	response    string
}

func (f *fakeLLMClient) ReviewCode(ctx context.Context, title, body, diff, fileContext string) (string, error) {
	f.title = title
	f.body = body
	f.diff = diff
	f.fileContext = fileContext
	return f.response, nil
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
	llm := &fakeLLMClient{response: "No blocking issues."}
	service := New(gh, llm, 100, 10, 100)

	if err := service.ReviewPR(context.Background(), "owner", "repo", 12, 1); err != nil {
		t.Fatalf("ReviewPR() error = %v", err)
	}
	if llm.title != "Fix login bug" || llm.body != "Fixes token validation." {
		t.Fatalf("unexpected PR metadata sent to LLM: %+v", llm)
	}
	if !strings.Contains(llm.diff, "internal/auth/auth.go") || !strings.Contains(llm.fileContext, "func ValidateToken") {
		t.Fatalf("unexpected model context: diff=%q fileContext=%q", llm.diff, llm.fileContext)
	}
	if !strings.Contains(gh.reviewBody, "No blocking issues.") ||
		!strings.Contains(gh.reviewBody, "Task ID: 1 | commit 291ac5a") {
		t.Fatalf("unexpected review comment: %s", gh.reviewBody)
	}
}
