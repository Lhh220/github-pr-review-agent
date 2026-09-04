package review

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/liaohonghui/github-pr-review-agent/internal/github"
	"github.com/liaohonghui/github-pr-review-agent/internal/llm"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

type GitHubClient interface {
	GetPullRequest(ctx context.Context, owner, repo string, number int) (*github.PullRequest, error)
	GetPullRequestFiles(ctx context.Context, owner, repo string, number int) ([]github.PullRequestFile, error)
	GetFileContent(ctx context.Context, owner, repo, path, ref string) (string, error)
	CreatePullRequestReview(ctx context.Context, owner, repo string, number int, body string) error
}

type LLMClient interface {
	ReviewCode(ctx context.Context, title, body, diff, fileContext string) (llm.ReviewResponse, error)
}

type ResultStore interface {
	CreateReviewResult(ctx context.Context, input store.NewReviewResult) (*store.ReviewResult, error)
}

type Service struct {
	GitHub              GitHubClient
	LLM                 LLMClient
	Results             ResultStore
	MaxDiffLines        int
	MaxFileContexts     int
	MaxFileContextLines int
}

func New(gh GitHubClient, l LLMClient, results ResultStore, maxDiffLines, maxFileContexts, maxFileContextLines int) *Service {
	return &Service{
		GitHub:              gh,
		LLM:                 l,
		Results:             results,
		MaxDiffLines:        maxDiffLines,
		MaxFileContexts:     maxFileContexts,
		MaxFileContextLines: maxFileContextLines,
	}
}

func (s *Service) ReviewPR(ctx context.Context, owner, repo string, number int, taskID uint64) error {
	pr, err := s.GitHub.GetPullRequest(ctx, owner, repo, number)
	if err != nil {
		return fmt.Errorf("get pull request: %w", err)
	}
	files, err := s.GitHub.GetPullRequestFiles(ctx, owner, repo, number)
	if err != nil {
		return fmt.Errorf("get pull request files: %w", err)
	}
	if len(files) == 0 {
		result, err := s.Results.CreateReviewResult(ctx, store.NewReviewResult{
			TaskID:      taskID,
			Summary:     "This pull request has no changed files relative to its base branch; review skipped.",
			Findings:    []store.Finding{},
			RawResponse: "No changed files relative to the base branch.",
			Model:       "none",
		})
		if err != nil {
			return fmt.Errorf("create no-diff review result: %w", err)
		}
		comment := buildReviewComment(*result, taskID, pr.Head.SHA)
		if err := s.GitHub.CreatePullRequestReview(ctx, owner, repo, number, comment); err != nil {
			return fmt.Errorf("create no-diff review: %w", err)
		}
		return nil
	}
	diff := buildDiff(files, s.MaxDiffLines)
	if strings.TrimSpace(diff) == "" {
		diff = "No textual diff was returned by GitHub."
	}
	contents := s.fetchFileContents(ctx, owner, repo, pr.Head.SHA, files)
	fileContext := buildFileContext(contents, s.MaxFileContextLines)
	if strings.TrimSpace(fileContext) == "" {
		fileContext = "No readable file context was available."
	}
	response, err := s.LLM.ReviewCode(ctx, pr.Title, pr.Body, diff, fileContext)
	if err != nil {
		return fmt.Errorf("review code: %w", err)
	}
	parsed := parseReviewResponse(response.Content)
	result, err := s.Results.CreateReviewResult(ctx, store.NewReviewResult{
		TaskID:        taskID,
		Summary:       parsed.Summary,
		Findings:      parsed.Findings,
		RawResponse:   response.Content,
		Model:         response.Model,
		InputTokens:   response.Usage.InputTokens,
		OutputTokens:  response.Usage.OutputTokens,
		TotalTokens:   response.Usage.TotalTokens,
		LLMDurationMS: response.DurationMS,
	})
	if err != nil {
		return fmt.Errorf("create review result: %w", err)
	}
	comment := buildReviewComment(*result, taskID, pr.Head.SHA)
	if err := s.GitHub.CreatePullRequestReview(ctx, owner, repo, number, comment); err != nil {
		return fmt.Errorf("create pull request review: %w", err)
	}
	return nil
}

type parsedReviewResponse struct {
	Summary  string          `json:"summary"`
	Findings []store.Finding `json:"findings"`
}

func parseReviewResponse(content string) parsedReviewResponse {
	cleaned := extractJSONObject(content)
	var parsed parsedReviewResponse
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil || strings.TrimSpace(parsed.Summary) == "" {
		return parsedReviewResponse{
			Summary:  content,
			Findings: []store.Finding{},
		}
	}
	if parsed.Findings == nil {
		parsed.Findings = []store.Finding{}
	}
	return parsed
}

func extractJSONObject(content string) string {
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "```") {
		lines := strings.Split(trimmed, "\n")
		if len(lines) > 1 {
			trimmed = strings.Join(lines[1:], "\n")
		}
		if idx := strings.LastIndex(trimmed, "```"); idx >= 0 {
			trimmed = trimmed[:idx]
		}
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		return trimmed[start : end+1]
	}
	return trimmed
}

func buildReviewComment(result store.ReviewResult, taskID uint64, commitSHA string) string {
	footer := fmt.Sprintf("Task ID: %d | commit %s", taskID, shortCommitSHA(commitSHA))
	var b strings.Builder
	b.WriteString("## Automated Code Review\n\n")
	b.WriteString(result.Summary)
	if len(result.Findings) > 0 {
		b.WriteString("\n\n### Findings\n")
		for i, finding := range result.Findings {
			b.WriteString(fmt.Sprintf(
				"\n%d. [%s / %s] `%s:%d` - %s\n",
				i+1,
				finding.Category,
				finding.Severity,
				finding.File,
				finding.Line,
				finding.Comment,
			))
			if finding.Suggestion != "" {
				b.WriteString(fmt.Sprintf("   Suggestion: %s\n", finding.Suggestion))
			}
		}
	} else {
		b.WriteString("\n\nNo issues found.")
	}
	b.WriteString(fmt.Sprintf("\n\n---\n%s", footer))
	return b.String()
}

func shortCommitSHA(commitSHA string) string {
	if len(commitSHA) >= 7 {
		return commitSHA[:7]
	}
	return commitSHA
}

func (s *Service) fetchFileContents(ctx context.Context, owner, repo, ref string, files []github.PullRequestFile) []github.FileContent {
	contents := make([]github.FileContent, 0)
	if s.MaxFileContexts <= 0 {
		return contents
	}
	for _, file := range files {
		if len(contents) >= s.MaxFileContexts {
			break
		}
		if !hasReadableExtension(file.Filename) {
			continue
		}
		content, err := s.GitHub.GetFileContent(ctx, owner, repo, file.Filename, ref)
		if err != nil {
			log.Printf("read file context failed: owner=%s repo=%s file=%s error=%v", owner, repo, file.Filename, err)
			continue
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		contents = append(contents, github.FileContent{Path: file.Filename, Content: content})
	}
	return contents
}

func buildFileContext(contents []github.FileContent, maxLines int) string {
	var b strings.Builder
	for _, content := range contents {
		b.WriteString(fmt.Sprintf("\n### %s\n", content.Path))
		b.WriteString(limitLines(content.Content, maxLines))
		b.WriteString("\n")
	}
	return b.String()
}

func limitLines(content string, maxLines int) string {
	if maxLines <= 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= maxLines {
		return content
	}
	return strings.Join(lines[:maxLines], "\n") + "\n[content truncated because it exceeded the configured line limit]"
}

func hasReadableExtension(filename string) bool {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".go", ".java", ".py", ".js", ".ts", ".tsx", ".jsx", ".rs", ".c", ".cc", ".cpp", ".h", ".hpp", ".rb", ".php", ".sql", ".yml", ".yaml", ".json", ".toml", ".md", ".txt", ".mod", ".sum":
		return true
	default:
		return false
	}
}

func buildDiff(files []github.PullRequestFile, maxLines int) string {
	var b strings.Builder
	lineCount := 0
	for _, f := range files {
		if maxLines > 0 && lineCount >= maxLines {
			b.WriteString("\n[diff truncated because it exceeded the configured line limit]\n")
			break
		}
		b.WriteString(fmt.Sprintf("\n### %s\n", f.Filename))
		b.WriteString(f.Patch)
		b.WriteString("\n")
		lineCount += strings.Count(f.Patch, "\n") + 1
	}
	return b.String()
}
