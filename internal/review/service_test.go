package review

import (
	"strings"
	"testing"

	"github.com/liaohonghui/github-pr-review-agent/internal/github"
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
