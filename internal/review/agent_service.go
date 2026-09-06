package review

import (
	"context"
	"fmt"
	"time"

	"github.com/liaohonghui/github-pr-review-agent/internal/agent"
	"github.com/liaohonghui/github-pr-review-agent/internal/github"
	"github.com/liaohonghui/github-pr-review-agent/internal/githubtools"
	"github.com/liaohonghui/github-pr-review-agent/internal/llm"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

type AgentGitHubClient interface {
	GetPullRequest(ctx context.Context, owner, repo string, number int) (*github.PullRequest, error)
	GetPullRequestFiles(ctx context.Context, owner, repo string, number int) ([]github.PullRequestFile, error)
	GetPullRequestCommits(ctx context.Context, owner, repo string, number, limit int) ([]github.PullRequestCommit, error)
	GetFileContent(ctx context.Context, owner, repo, path, ref string) (string, error)
	CreatePullRequestReview(ctx context.Context, owner, repo string, number int, body string) error
}

type AgentProvider interface {
	ChatWithTools(ctx context.Context, request llm.ChatRequest) (llm.ChatResponse, error)
}

type AgentStore interface {
	CreateReviewResult(ctx context.Context, input store.NewReviewResult) (*store.ReviewResult, error)
	CreateToolCallLog(ctx context.Context, input store.NewToolCallLog) (*store.ToolCallLog, error)
}

type AgentOptions struct {
	MaxSteps            int
	ToolTimeout         time.Duration
	MaxDiffLines        int
	MaxFileContextLines int
	MaxCommitHistory    int
}

type AgentService struct {
	GitHub   AgentGitHubClient
	Provider AgentProvider
	Store    AgentStore
	Options  AgentOptions
}

func NewAgent(
	gh AgentGitHubClient,
	provider AgentProvider,
	resultStore AgentStore,
	options AgentOptions,
) *AgentService {
	return &AgentService{
		GitHub:   gh,
		Provider: provider,
		Store:    resultStore,
		Options:  options,
	}
}

func (s *AgentService) ReviewPR(ctx context.Context, owner, repo string, number int, taskID uint64) error {
	toolkit := githubtools.NewToolkit(s.GitHub, owner, repo, number, githubtools.Options{
		MaxDiffLines:        s.Options.MaxDiffLines,
		MaxFileContextLines: s.Options.MaxFileContextLines,
		MaxCommitHistory:    s.Options.MaxCommitHistory,
	})
	registry, err := agent.NewRegistry(toolkit.Tools()...)
	if err != nil {
		return fmt.Errorf("register github tools: %w", err)
	}
	agentRunner, err := agent.New(s.Provider, registry, agent.Options{
		MaxSteps:    s.Options.MaxSteps,
		ToolTimeout: s.Options.ToolTimeout,
		OnToolCall: func(ctx context.Context, invocation agent.ToolInvocation) error {
			return s.recordToolCall(ctx, taskID, invocation)
		},
	})
	if err != nil {
		return fmt.Errorf("create review agent: %w", err)
	}

	result, err := agentRunner.Run(ctx, agent.Request{
		SystemPrompt: agentSystemPrompt(),
		UserPrompt:   fmt.Sprintf("Review pull request %s/%s#%d using the available tools.", owner, repo, number),
		MaxSteps:     s.Options.MaxSteps,
		ToolTimeout:  s.Options.ToolTimeout,
	})
	if err != nil {
		return fmt.Errorf("run review agent: %w", err)
	}

	parsed := parseReviewResponse(result.Content)
	stored, err := s.Store.CreateReviewResult(ctx, store.NewReviewResult{
		TaskID:        taskID,
		Summary:       parsed.Summary,
		Findings:      parsed.Findings,
		RawResponse:   result.Content,
		Model:         result.Model,
		InputTokens:   result.Usage.InputTokens,
		OutputTokens:  result.Usage.OutputTokens,
		TotalTokens:   result.Usage.TotalTokens,
		LLMDurationMS: result.DurationMS,
	})
	if err != nil {
		return fmt.Errorf("create agent review result: %w", err)
	}

	pr, err := toolkit.PullRequest(ctx)
	if err != nil {
		return fmt.Errorf("get pull request after review: %w", err)
	}
	comment := buildReviewComment(*stored, taskID, pr.Head.SHA)
	if err := s.GitHub.CreatePullRequestReview(ctx, owner, repo, number, comment); err != nil {
		return fmt.Errorf("create pull request review: %w", err)
	}
	return nil
}

func (s *AgentService) recordToolCall(ctx context.Context, taskID uint64, invocation agent.ToolInvocation) error {
	_, err := s.Store.CreateToolCallLog(ctx, store.NewToolCallLog{
		TaskID:     taskID,
		ToolName:   invocation.Name,
		Input:      invocation.Arguments,
		Output:     invocation.Output,
		Error:      invocation.Error,
		DurationMS: invocation.DurationMS,
	})
	if err != nil {
		return fmt.Errorf("create tool call log: %w", err)
	}
	return nil
}

func agentSystemPrompt() string {
	return `You are a senior code reviewer for a Go backend project.

Use the available read-only GitHub tools to inspect the pull request before reaching a conclusion. Start by understanding the PR metadata and changed files, then read only the diffs and file context needed to validate possible issues. Do not invent tool results.

Return only a valid JSON object matching this schema:
{
  "summary": "short overall review summary",
  "findings": [
    {
      "category": "bug|performance|security|style",
      "file": "path/to/file.go",
      "line": 12,
      "severity": "high|medium|low",
      "comment": "specific issue",
      "suggestion": "optional fix suggestion",
      "confidence": "confirmed|needs_verification"
    }
  ]
}

Focus on real bugs, performance issues, security risks, and important readability problems. If the code looks good, return an empty findings array. Be concise and specific.`
}
