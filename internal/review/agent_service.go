package review

import (
	"context"
	"fmt"
	"io"
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
	GetRepositoryTarball(ctx context.Context, owner, repo, ref string) (io.ReadCloser, error)
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
	MaxReferenceResults int
	EnableStaticChecks  bool
	StaticCheckTimeout  time.Duration
	StaticCheckWorkDir  string
	StaticCheckGoProxy  string
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
		MaxReferenceResults: s.Options.MaxReferenceResults,
		EnableStaticChecks:  s.Options.EnableStaticChecks,
		StaticCheckTimeout:  s.Options.StaticCheckTimeout,
		StaticCheckWorkDir:  s.Options.StaticCheckWorkDir,
		StaticCheckGoProxy:  s.Options.StaticCheckGoProxy,
	})
	pr, err := toolkit.PullRequest(ctx)
	if err != nil {
		return fmt.Errorf("get pull request before agent review: %w", err)
	}
	files, err := toolkit.Files(ctx)
	if err != nil {
		return fmt.Errorf("get pull request files before agent review: %w", err)
	}
	if len(files) == 0 || isDocsOnlyPR(files) {
		summary := "This pull request has no changed files relative to its base branch; review skipped."
		rawResponse := "No changed files relative to the base branch."
		if len(files) > 0 {
			summary = "This pull request only changes documentation; code review skipped."
			rawResponse = "Documentation-only pull request; code review skipped."
		}
		stored, err := s.Store.CreateReviewResult(ctx, store.NewReviewResult{
			TaskID:      taskID,
			Summary:     summary,
			Findings:    []store.Finding{},
			RawResponse: rawResponse,
			Model:       "none",
		})
		if err != nil {
			return fmt.Errorf("create skipped agent review result: %w", err)
		}
		comment := buildReviewComment(*stored, taskID, pr.Head.SHA)
		if err := s.GitHub.CreatePullRequestReview(ctx, owner, repo, number, comment); err != nil {
			return fmt.Errorf("create skipped pull request review: %w", err)
		}
		return nil
	}
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

	toolOutputs := make([]string, 0, len(result.ToolCalls))
	for _, invocation := range result.ToolCalls {
		if invocation.Error == "" {
			toolOutputs = append(toolOutputs, invocation.Output)
		}
	}
	parsed, err := parseReviewResponse(result.Content, toolOutputs...)
	if err != nil {
		return err
	}
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

	pr, err = toolkit.PullRequest(ctx)
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

Use the available read-only GitHub tools to inspect the pull request before reaching a conclusion. Start by understanding the PR metadata and changed files, then read only the diffs and file context needed to validate possible issues. Prefer read_file_context for function or class context instead of judging from the diff alone. When a diff removes, renames, or changes an exported symbol, field, method, type, or important config key, use search_references to check remaining usages before reporting the issue. If run_static_checks is available and the PR may affect compilation, tests, or static analysis, run the relevant server-defined check and use its result as deterministic evidence. Do not invent tool results.

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
      "confidence": "confirmed|needs_verification",
      "evidence": [
        {
          "type": "reference|static_check",
          "file": "path/to/file.go",
          "line": 12,
          "text": "exact source line from a tool result"
        },
        {
          "type": "static_check",
          "command": "go test ./...",
          "excerpt": "exact failure output from the tool"
        }
      ]
    }
  ]
}

Rules:
- Every finding must include non-empty evidence copied exactly from a tool result; do not paraphrase or invent evidence.
- Use confirmed only when a tool result proves the issue, such as a remaining cross-file reference or a failed static check. Without deterministic tool evidence, use needs_verification.
- Treat architectural concerns, performance risks, and concurrency concerns that need human confirmation as needs_verification.
- Do not report pure formatting or style preferences.
- Do not invent files, line numbers, commands, or output.
- Reference evidence must use the same file and line as the finding and quote the source exactly.
- Static-check evidence must quote the failed command and output exactly. If evidence cannot be quoted exactly, omit the finding.
- Report at most five findings, and only report issues introduced or directly triggered by this pull request.
- Prioritize bugs, security risks, and performance issues over style.
- If every changed file is documentation-only, return an empty findings array.
- If the code looks good, return an empty findings array.
Be concise and specific.`
}
