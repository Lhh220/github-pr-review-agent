package githubtools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/liaohonghui/github-pr-review-agent/internal/github"
)

type fakeClient struct {
	pr           *github.PullRequest
	files        []github.PullRequestFile
	commits      []github.PullRequestCommit
	fileContents map[string]string

	getPRCalls    int
	getFilesCalls int
	owners        []string
	repos         []string
	numbers       []int
	commitLimits  []int
	filePaths     []string
	refs          []string
}

func (f *fakeClient) GetPullRequest(ctx context.Context, owner, repo string, number int) (*github.PullRequest, error) {
	f.getPRCalls++
	f.owners = append(f.owners, owner)
	f.repos = append(f.repos, repo)
	f.numbers = append(f.numbers, number)
	return f.pr, nil
}

func (f *fakeClient) GetPullRequestFiles(ctx context.Context, owner, repo string, number int) ([]github.PullRequestFile, error) {
	f.getFilesCalls++
	return f.files, nil
}

func (f *fakeClient) GetPullRequestCommits(ctx context.Context, owner, repo string, number, limit int) ([]github.PullRequestCommit, error) {
	f.commitLimits = append(f.commitLimits, limit)
	return f.commits, nil
}

func (f *fakeClient) GetFileContent(ctx context.Context, owner, repo, path, ref string) (string, error) {
	f.filePaths = append(f.filePaths, path)
	f.refs = append(f.refs, ref)
	return f.fileContents[path], nil
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		pr: &github.PullRequest{
			Number: 12,
			Title:  "Fix token validation",
			Body:   "Validate tokens correctly.",
			Head:   github.Ref{SHA: "head-sha", Ref: "feature"},
			Base:   github.Ref{SHA: "base-sha", Ref: "main"},
		},
		files: []github.PullRequestFile{
			{Filename: "internal/auth/auth.go", Status: "modified", Additions: 3, Deletions: 1, Changes: 4, Patch: "@@ -1,2 +1,3 @@\n-old\n+new"},
			{Filename: "internal/auth/token.go", Status: "added", Additions: 2, Deletions: 0, Changes: 2, Patch: "@@ -0,0 +1,2 @@\n+func Token() {}"},
		},
		commits: []github.PullRequestCommit{
			{SHA: "commit-1", Commit: github.CommitDetail{Message: "fix validation", Author: github.CommitAuthor{Name: "Lhh"}}},
		},
		fileContents: map[string]string{
			"internal/auth/auth.go": strings.Repeat("line\n", 10),
		},
	}
}

func toolByName(t *testing.T, toolkit *Toolkit, name string) interface {
	Execute(context.Context, map[string]any) (string, error)
} {
	t.Helper()
	for _, tool := range toolkit.Tools() {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("tool %q was not registered", name)
	return nil
}

func TestToolkitRegistersFiveReadOnlyTools(t *testing.T) {
	toolkit := NewToolkit(newFakeClient(), "owner", "repo", 12, Options{})
	tools := toolkit.Tools()
	expected := []string{
		"get_pr_meta",
		"list_changed_files",
		"read_diff",
		"read_file_context",
		"get_commit_history",
	}
	if len(tools) != len(expected) {
		t.Fatalf("tool count = %d, want %d", len(tools), len(expected))
	}
	for i, tool := range tools {
		if tool.Name() != expected[i] || tool.Description() == "" || len(tool.Schema()) == 0 {
			t.Fatalf("unexpected tool at %d: %+v", i, tool)
		}
	}
}

func TestToolkitCachesPRAndFiles(t *testing.T) {
	client := newFakeClient()
	toolkit := NewToolkit(client, "owner", "repo", 12, Options{})
	ctx := context.Background()

	if _, err := toolkit.PullRequest(ctx); err != nil {
		t.Fatalf("get pull request: %v", err)
	}
	if _, err := toolByName(t, toolkit, "get_pr_meta").Execute(ctx, map[string]any{}); err != nil {
		t.Fatalf("get_pr_meta: %v", err)
	}
	if _, err := toolByName(t, toolkit, "list_changed_files").Execute(ctx, map[string]any{}); err != nil {
		t.Fatalf("list_changed_files: %v", err)
	}
	if _, err := toolByName(t, toolkit, "read_diff").Execute(ctx, map[string]any{}); err != nil {
		t.Fatalf("read_diff: %v", err)
	}

	if client.getPRCalls != 1 || client.getFilesCalls != 1 {
		t.Fatalf("github calls were not cached: pr=%d files=%d", client.getPRCalls, client.getFilesCalls)
	}
	if len(client.owners) != 1 || client.owners[0] != "owner" || client.repos[0] != "repo" || client.numbers[0] != 12 {
		t.Fatalf("unexpected github context: %+v", client)
	}
}

func TestFileContextToolCapsRangeAndNumbersLines(t *testing.T) {
	client := newFakeClient()
	toolkit := NewToolkit(client, "owner", "repo", 12, Options{MaxFileContextLines: 3})
	output, err := toolByName(t, toolkit, "read_file_context").Execute(context.Background(), map[string]any{
		"path":       "internal/auth/auth.go",
		"start_line": 2,
		"end_line":   9,
	})
	if err != nil {
		t.Fatalf("read_file_context: %v", err)
	}

	var result struct {
		StartLine   int    `json:"start_line"`
		EndLine     int    `json:"end_line"`
		RangeCapped bool   `json:"range_capped"`
		Content     string `json:"content"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if result.StartLine != 2 || result.EndLine != 4 || !result.RangeCapped {
		t.Fatalf("unexpected range: %+v", result)
	}
	if !strings.Contains(result.Content, "2: line") || !strings.Contains(result.Content, "4: line") ||
		strings.Contains(result.Content, "5: line") {
		t.Fatalf("unexpected numbered content: %q", result.Content)
	}
	if len(client.filePaths) != 1 || client.filePaths[0] != "internal/auth/auth.go" || client.refs[0] != "head-sha" {
		t.Fatalf("unexpected file request: %+v", client)
	}
}

func TestDiffToolFiltersAndBoundsFullDiff(t *testing.T) {
	client := newFakeClient()
	toolkit := NewToolkit(client, "owner", "repo", 12, Options{MaxDiffLines: 2})
	ctx := context.Background()

	oneFileOutput, err := toolByName(t, toolkit, "read_diff").Execute(ctx, map[string]any{
		"path": "internal/auth/token.go",
	})
	if err != nil {
		t.Fatalf("read one diff: %v", err)
	}
	var oneFile struct {
		Path      string `json:"path"`
		Found     bool   `json:"found"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(oneFileOutput), &oneFile); err != nil {
		t.Fatalf("decode one diff: %v", err)
	}
	if oneFile.Path != "internal/auth/token.go" || !oneFile.Found || oneFile.Truncated {
		t.Fatalf("unexpected one-file diff: %+v", oneFile)
	}

	fullOutput, err := toolByName(t, toolkit, "read_diff").Execute(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("read full diff: %v", err)
	}
	var full struct {
		Truncated bool `json:"truncated"`
		Files     []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(fullOutput), &full); err != nil {
		t.Fatalf("decode full diff: %v", err)
	}
	if !full.Truncated || len(full.Files) != 1 || full.Files[0].Path != "internal/auth/auth.go" {
		t.Fatalf("unexpected bounded full diff: %+v", full)
	}
}

func TestCommitHistoryToolCapsLimit(t *testing.T) {
	client := newFakeClient()
	toolkit := NewToolkit(client, "owner", "repo", 12, Options{MaxCommitHistory: 1})
	output, err := toolByName(t, toolkit, "get_commit_history").Execute(context.Background(), map[string]any{
		"limit": 10,
	})
	if err != nil {
		t.Fatalf("get_commit_history: %v", err)
	}
	var result struct {
		Limit       int              `json:"limit"`
		LimitCapped bool             `json:"limit_capped"`
		Commits     []map[string]any `json:"commits"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if result.Limit != 1 || !result.LimitCapped || len(result.Commits) != 1 {
		t.Fatalf("unexpected commit history: %+v", result)
	}
}

func TestToolsRejectNonIntegerArguments(t *testing.T) {
	toolkit := NewToolkit(newFakeClient(), "owner", "repo", 12, Options{})
	_, err := toolByName(t, toolkit, "read_file_context").Execute(context.Background(), map[string]any{
		"path":       "internal/auth/auth.go",
		"start_line": "two",
	})
	if err == nil || !strings.Contains(err.Error(), "start_line must be an integer") {
		t.Fatalf("expected integer validation error, got %v", err)
	}
}
