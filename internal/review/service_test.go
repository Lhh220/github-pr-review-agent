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
		Summary: "One issue.",
		Findings: []store.Finding{{
			Category:   "bug",
			File:       "cmd/server/main.go",
			Line:       42,
			Severity:   "high",
			Comment:    "Field is still referenced.",
			Confidence: "confirmed",
			Evidence: []store.Evidence{{
				Type: "reference",
				File: "cmd/server/main.go",
				Line: 42,
				Text: "cfg.MaxDiffLines",
			}},
		}},
	}
	got := buildReviewComment(result, 1, "291ac5aedc5fd96c5030a6c18e91923140677591")
	if !strings.Contains(got, "## Automated Code Review") ||
		!strings.Contains(got, "One issue.") ||
		!strings.Contains(got, "bug / high / confirmed") ||
		!strings.Contains(got, "Evidence: `cmd/server/main.go:42`: cfg.MaxDiffLines") ||
		!strings.Contains(got, "Task ID: 1 | commit 291ac5a") {
		t.Fatalf("unexpected review comment: %s", got)
	}
}

func TestParseReviewResponseRequiresEvidence(t *testing.T) {
	content := `{
		"summary": "Two useful findings and one unsupported finding.",
		"findings": [
			{
				"category": "bug",
				"file": "cmd/server/main.go",
				"line": 42,
				"severity": "high",
				"comment": "MaxDiffLines is still referenced after removal.",
				"suggestion": "Restore the field or remove the reference.",
				"confidence": "confirmed",
				"evidence": [
					{"type": "reference", "file": "cmd/server/main.go", "line": 42, "text": "cfg.MaxDiffLines"},
					{"type": "static_check", "command": "go test ./...", "excerpt": "undefined: cfg.MaxDiffLines"}
				]
			},
			{
				"category": "performance",
				"file": "internal/review/service.go",
				"line": 20,
				"severity": "medium",
				"comment": "The scan may become slow for large pull requests.",
				"confidence": "needs_verification",
				"evidence": [
					{"type": "reference", "file": "internal/review/service.go", "line": 20, "text": "for _, file := range files"}
				]
			},
			{
				"category": "bug",
				"file": "internal/missing/missing.go",
				"line": 1,
				"severity": "low",
				"comment": "No evidence was supplied.",
				"confidence": "confirmed",
				"evidence": []
			}
		]
	}`

	parsed := parseReviewResponse(
		content,
		"cfg.MaxDiffLines",
		`{"checks":[{"command":"go test ./...","output":"undefined: cfg.MaxDiffLines"}]}`,
		"for _, file := range files",
	)
	if len(parsed.Findings) != 2 {
		t.Fatalf("findings length = %d, want 2: %+v", len(parsed.Findings), parsed.Findings)
	}
	confirmed := parsed.Findings[0]
	if confirmed.Confidence != "confirmed" || len(confirmed.Evidence) != 2 ||
		confirmed.Evidence[0].Text != "cfg.MaxDiffLines" ||
		confirmed.Evidence[1].Command != "go test ./..." {
		t.Fatalf("unexpected confirmed finding: %+v", confirmed)
	}
	performance := parsed.Findings[1]
	if performance.Confidence != "needs_verification" || len(performance.Evidence) != 1 {
		t.Fatalf("unexpected performance finding: %+v", performance)
	}
}

func TestParseReviewResponseDropsFabricatedEvidence(t *testing.T) {
	content := `{
		"summary": "Fabricated evidence is not retained.",
		"findings": [
			{
				"category": "bug",
				"file": "cmd/server/main.go",
				"line": 42,
				"severity": "high",
				"comment": "Fabricated source line.",
				"confidence": "confirmed",
				"evidence": [
					{"type": "reference", "file": "cmd/server/main.go", "line": 42, "text": "not in the tool output"}
				]
			},
			{
				"category": "performance",
				"file": "internal/review/service.go",
				"line": 20,
				"severity": "medium",
				"comment": "Performance findings remain speculative without a benchmark.",
				"confidence": "confirmed",
				"evidence": [
					{"type": "reference", "file": "internal/review/service.go", "line": 20, "text": "for _, file := range files"}
				]
			}
		]
	}`

	parsed := parseReviewResponse(content, "for _, file := range files")
	if len(parsed.Findings) != 1 {
		t.Fatalf("findings length = %d, want 1: %+v", len(parsed.Findings), parsed.Findings)
	}
	if parsed.Findings[0].Confidence != "needs_verification" {
		t.Fatalf("performance confidence = %s, want needs_verification", parsed.Findings[0].Confidence)
	}
}

func TestIsDocsOnlyPR(t *testing.T) {
	tests := []struct {
		name  string
		files []github.PullRequestFile
		want  bool
	}{
		{name: "markdown only", files: []github.PullRequestFile{{Filename: "README.md"}}, want: true},
		{name: "mixed code", files: []github.PullRequestFile{{Filename: "README.md"}, {Filename: "main.go"}}, want: false},
		{name: "no files", files: nil, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isDocsOnlyPR(test.files); got != test.want {
				t.Fatalf("isDocsOnlyPR() = %t, want %t", got, test.want)
			}
		})
	}
}
