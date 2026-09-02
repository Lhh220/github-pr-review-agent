package review

import (
	"context"
	"fmt"
	"strings"

	"github.com/liaohonghui/github-pr-review-agent/internal/github"
	"github.com/liaohonghui/github-pr-review-agent/internal/llm"
)

type Service struct {
	GitHub      *github.Client
	LLM         *llm.Client
	MaxDiffLines int
}

func New(gh *github.Client, l *llm.Client, maxDiffLines int) *Service {
	return &Service{GitHub: gh, LLM: l, MaxDiffLines: maxDiffLines}
}

func (s *Service) ReviewPR(ctx context.Context, owner, repo string, number int) error {
	pr, err := s.GitHub.GetPullRequest(ctx, owner, repo, number)
	if err != nil {
		return fmt.Errorf("get pull request: %w", err)
	}
	files, err := s.GitHub.GetPullRequestFiles(ctx, owner, repo, number)
	if err != nil {
		return fmt.Errorf("get pull request files: %w", err)
	}
	diff := buildDiff(files, s.MaxDiffLines)
	if strings.TrimSpace(diff) == "" {
		diff = "No textual diff was returned by GitHub."
	}
	reviewText, err := s.LLM.ReviewCode(ctx, pr.Title, pr.Body, diff)
	if err != nil {
		return fmt.Errorf("review code: %w", err)
	}
	comment := "## Automated Code Review\n\n" + reviewText
	if err := s.GitHub.CreateIssueComment(ctx, owner, repo, number, comment); err != nil {
		return fmt.Errorf("create comment: %w", err)
	}
	return nil
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
