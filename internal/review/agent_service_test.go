package review

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/liaohonghui/github-pr-review-agent/internal/github"
	"github.com/liaohonghui/github-pr-review-agent/internal/llm"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

func TestInvalidOutputDoesNotPublishOrPersistReview(t *testing.T) {
	for _, content := range []string{"not JSON", `{"summary":"fine"}`, `{"summary":"fine","findings":null}`, `{"summary":"","findings":[]}`, `{"summary":"fine","findings":{}}`} {
		t.Run(content, func(t *testing.T) {
			pr := &github.PullRequest{Head: github.Ref{SHA: "head"}}
			files := []github.PullRequestFile{{Filename: "a.go", Patch: "@@ -1 +1 @@\n+x"}}
			legacyGH := &fakeGitHubClient{pr: pr, files: files}
			legacyStore := &fakeResultStore{}
			legacy := New(legacyGH, &fakeLLMClient{response: llm.ReviewResponse{Content: content}}, legacyStore, 100, 0, 100)
			if err := legacy.ReviewPR(context.Background(), "o", "r", 1, 1); err == nil {
				t.Fatal("legacy accepted invalid output")
			}
			if legacyGH.reviewBody != "" || legacyStore.input.TaskID != 0 {
				t.Fatal("legacy published or persisted invalid review")
			}
			agentGH := &fakeAgentGitHubClient{pr: pr, files: files}
			agentStore := &fakeAgentStore{}
			service := NewAgent(agentGH, &scriptedAgentProvider{responses: []llm.ChatResponse{{Content: content}}}, agentStore, AgentOptions{})
			if err := service.ReviewPR(context.Background(), "o", "r", 1, 1); err == nil {
				t.Fatal("agent accepted invalid output")
			}
			if agentGH.reviewBody != "" || agentStore.result.TaskID != 0 {
				t.Fatal("agent published or persisted invalid review")
			}
		})
	}
}

type fakeAgentGitHubClient struct {
	pr          *github.PullRequest
	files       []github.PullRequestFile
	commits     []github.PullRequestCommit
	content     string
	reviewBody  string
	contentPath string
	contentRef  string
}

func (f *fakeAgentGitHubClient) GetPullRequest(ctx context.Context, owner, repo string, number int) (*github.PullRequest, error) {
	return f.pr, nil
}

func (f *fakeAgentGitHubClient) GetPullRequestFiles(ctx context.Context, owner, repo string, number int) ([]github.PullRequestFile, error) {
	return f.files, nil
}

func (f *fakeAgentGitHubClient) GetPullRequestCommits(ctx context.Context, owner, repo string, number, limit int) ([]github.PullRequestCommit, error) {
	return f.commits, nil
}

func (f *fakeAgentGitHubClient) GetFileContent(ctx context.Context, owner, repo, path, ref string) (string, error) {
	f.contentPath = path
	f.contentRef = ref
	return f.content, nil
}

func (f *fakeAgentGitHubClient) GetRepositoryTarball(ctx context.Context, owner, repo, ref string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (f *fakeAgentGitHubClient) CreatePullRequestReview(ctx context.Context, owner, repo string, number int, body string) error {
	f.reviewBody = body
	return nil
}

type scriptedAgentProvider struct {
	responses []llm.ChatResponse
	requests  []llm.ChatRequest
}

func (p *scriptedAgentProvider) ChatWithTools(ctx context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	p.requests = append(p.requests, request)
	if len(p.responses) == 0 {
		return llm.ChatResponse{}, context.Canceled
	}
	response := p.responses[0]
	p.responses = p.responses[1:]
	return response, nil
}

type fakeAgentStore struct {
	delivery  *store.ReviewDelivery
	result    store.NewReviewResult
	toolCalls []store.NewToolCallLog
}

func (f *fakeAgentStore) CreateReviewResult(ctx context.Context, input store.NewReviewResult) (*store.ReviewResult, error) {
	f.result = input
	return &store.ReviewResult{
		ID:        1,
		TaskID:    input.TaskID,
		Summary:   input.Summary,
		Findings:  input.Findings,
		CreatedAt: time.Now(),
	}, nil
}

func (f *fakeAgentStore) CreateToolCallLog(ctx context.Context, input store.NewToolCallLog) (*store.ToolCallLog, error) {
	f.toolCalls = append(f.toolCalls, input)
	return &store.ToolCallLog{ID: uint64(len(f.toolCalls))}, nil
}

func TestAgentReviewPRRunsToolsAndPersistsTrace(t *testing.T) {
	gh := &fakeAgentGitHubClient{
		pr: &github.PullRequest{
			Title: "Fix token validation",
			Head:  github.Ref{SHA: "291ac5aedc5fd96c5030a6c18e91923140677591"},
		},
		files: []github.PullRequestFile{
			{Filename: "internal/auth/auth.go", Patch: "@@ -1 +1 @@\n+validate token"},
		},
		content: "func ValidateToken() {}",
	}
	provider := &scriptedAgentProvider{responses: []llm.ChatResponse{
		{
			ToolCalls:  []llm.ToolCall{{ID: "call-1", Name: "list_changed_files", Arguments: `{}`}},
			Usage:      llm.Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
			Model:      "deepseek-chat",
			DurationMS: 100,
		},
		{
			ToolCalls: []llm.ToolCall{{
				ID:        "call-2",
				Name:      "read_file_context",
				Arguments: `{"path":"internal/auth/auth.go","start_line":1,"end_line":20}`,
			}},
			Usage:      llm.Usage{InputTokens: 20, OutputTokens: 2, TotalTokens: 22},
			DurationMS: 120,
		},
		{
			Content:    `{"summary":"No blocking issues.","findings":[]}`,
			Usage:      llm.Usage{InputTokens: 30, OutputTokens: 8, TotalTokens: 38},
			Model:      "deepseek-chat",
			DurationMS: 200,
		},
	}}
	resultStore := &fakeAgentStore{}
	service := NewAgent(gh, provider, resultStore, AgentOptions{
		MaxSteps:            5,
		MaxDiffLines:        100,
		MaxFileContextLines: 20,
		MaxCommitHistory:    5,
	})

	if err := service.ReviewPR(context.Background(), "owner", "repo", 12, 7); err != nil {
		t.Fatalf("ReviewPR() error = %v", err)
	}
	if len(provider.requests) != 3 || len(provider.requests[0].Tools) != 6 {
		t.Fatalf("unexpected provider requests: count=%d first_tools=%d", len(provider.requests), len(provider.requests[0].Tools))
	}
	if provider.requests[1].Messages[3].Role != "tool" ||
		!strings.Contains(provider.requests[1].Messages[3].Content, "internal/auth/auth.go") {
		t.Fatalf("unexpected changed-files result: %+v", provider.requests[1].Messages)
	}
	if provider.requests[2].Messages[5].Role != "tool" ||
		!strings.Contains(provider.requests[2].Messages[5].Content, "ValidateToken") {
		t.Fatalf("unexpected file-context result: %+v", provider.requests[2].Messages)
	}
	if gh.contentPath != "internal/auth/auth.go" || gh.contentRef != "291ac5aedc5fd96c5030a6c18e91923140677591" {
		t.Fatalf("unexpected file context request: path=%s ref=%s", gh.contentPath, gh.contentRef)
	}
	if len(resultStore.toolCalls) != 2 ||
		resultStore.toolCalls[0].ToolName != "list_changed_files" ||
		resultStore.toolCalls[1].ToolName != "read_file_context" {
		t.Fatalf("unexpected tool call log: %+v", resultStore.toolCalls)
	}
	if resultStore.result.Summary != "No blocking issues." ||
		resultStore.result.InputTokens != 60 || resultStore.result.TotalTokens != 72 ||
		resultStore.result.Model != "deepseek-chat" || resultStore.result.LLMDurationMS != 420 {
		t.Fatalf("unexpected result input: %+v", resultStore.result)
	}
	if !strings.Contains(gh.reviewBody, "No blocking issues.") ||
		!strings.Contains(gh.reviewBody, "Task ID: 7 | commit 291ac5a") {
		t.Fatalf("unexpected review comment: %s", gh.reviewBody)
	}
}

func TestAgentServiceRegistersStaticChecksWhenEnabled(t *testing.T) {
	gh := &fakeAgentGitHubClient{
		pr: &github.PullRequest{
			Title: "No-op",
			Head:  github.Ref{SHA: "291ac5aedc5fd96c5030a6c18e91923140677591"},
		},
		files: []github.PullRequestFile{{Filename: "main.go", Patch: "@@ -1 +1 @@\n+package main"}},
	}
	provider := &scriptedAgentProvider{responses: []llm.ChatResponse{
		{Content: `{"summary":"No blocking issues.","findings":[]}`},
	}}
	service := NewAgent(gh, provider, &fakeAgentStore{}, AgentOptions{
		EnableStaticChecks: true,
		StaticCheckWorkDir: t.TempDir(),
	})

	if err := service.ReviewPR(context.Background(), "owner", "repo", 12, 8); err != nil {
		t.Fatalf("ReviewPR() error = %v", err)
	}
	if len(provider.requests) == 0 || len(provider.requests[0].Tools) != 7 {
		t.Fatalf("unexpected tool definitions: requests=%d tools=%d", len(provider.requests), len(provider.requests[0].Tools))
	}
}

func TestAgentReviewPRSkipsNoDiffAndDocsOnly(t *testing.T) {
	tests := []struct {
		name    string
		files   []github.PullRequestFile
		summary string
	}{
		{
			name:    "no changed files",
			files:   nil,
			summary: "This pull request has no changed files relative to its base branch; review skipped.",
		},
		{
			name:    "documentation only",
			files:   []github.PullRequestFile{{Filename: "README.md", Patch: "@@ -1 +1 @@\n+updated"}},
			summary: "This pull request only changes documentation; code review skipped.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gh := &fakeAgentGitHubClient{
				pr: &github.PullRequest{
					Title: "No-op",
					Head:  github.Ref{SHA: "291ac5aedc5fd96c5030a6c18e91923140677591"},
				},
				files: test.files,
			}
			provider := &scriptedAgentProvider{}
			resultStore := &fakeAgentStore{}
			service := NewAgent(gh, provider, resultStore, AgentOptions{})

			if err := service.ReviewPR(context.Background(), "owner", "repo", 12, 9); err != nil {
				t.Fatalf("ReviewPR() error = %v", err)
			}
			if len(provider.requests) != 0 {
				t.Fatalf("provider was called for a skipped pull request: %+v", provider.requests)
			}
			if len(resultStore.toolCalls) != 0 || len(resultStore.result.Findings) != 0 ||
				resultStore.result.Model != "none" || resultStore.result.Summary != test.summary {
				t.Fatalf("unexpected skipped result: %+v", resultStore)
			}
			if !strings.Contains(gh.reviewBody, "review skipped") {
				t.Fatalf("unexpected skipped review comment: %s", gh.reviewBody)
			}
		})
	}
}

func TestAgentSystemPromptRequiresEvidenceAndConfidenceLevels(t *testing.T) {
	prompt := agentSystemPrompt()
	for _, required := range []string{
		`"evidence"`,
		"Every finding must include non-empty evidence",
		"Use confirmed only when a tool result proves the issue",
		"performance risks",
		"needs_verification",
		"documentation-only",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("system prompt missing %q: %s", required, prompt)
		}
	}
}
