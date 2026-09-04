package review

import (
	"strings"
	"testing"

	"github.com/liaohonghui/github-pr-review-agent/internal/github"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

func TestBuildDiff(t *testing.T) {
	files := []github.PullRequestFile{
		{Filename: "a.go", Patch: "diff line 1\ndiff line 2"},
		{Filename: "b.go", Patch: "diff line 3"},
	}
	got := buildDiff(files, 0)
	if !strings.Contains(got, "### a.go") || !strings.Contains(got, "### b.go") {
		t.Fatalf("unexpected diff: %s", got)
	}
}

func TestBuildDiffTruncates(t *testing.T) {
	files := []github.PullRequestFile{
		{Filename: "a.go", Patch: "line1\nline2\nline3\nline4"},
		{Filename: "b.go", Patch: "line5"},
	}
	got := buildDiff(files, 3)
	if !strings.Contains(got, "diff truncated") {
		t.Fatalf("expected truncation marker, got: %s", got)
	}
}

func TestBuildFileContext(t *testing.T) {
	contents := []github.FileContent{
		{Path: "internal/config/config.go", Content: "line 1\nline 2"},
	}
	got := buildFileContext(contents, 10)
	if !strings.Contains(got, "### internal/config/config.go") || !strings.Contains(got, "line 1") {
		t.Fatalf("unexpected file context: %s", got)
	}
}

func TestBuildFileContextTruncates(t *testing.T) {
	contents := []github.FileContent{
		{Path: "a.go", Content: "1\n2\n3\n4\n5"},
	}
	got := buildFileContext(contents, 3)
	if !strings.Contains(got, "content truncated") {
		t.Fatalf("expected truncation marker, got: %s", got)
	}
}

func TestBuildReviewComment(t *testing.T) {
	result := store.ReviewResult{
		Summary:  "No issues.",
		Findings: []store.Finding{},
	}
	got := buildReviewComment(result, 1, "291ac5aedc5fd96c5030a6c18e91923140677591")
	if !strings.Contains(got, "## Automated Code Review") ||
		!strings.Contains(got, "No issues.") ||
		!strings.Contains(got, "Task ID: 1 | commit 291ac5a") {
		t.Fatalf("unexpected review comment: %s", got)
	}
}
