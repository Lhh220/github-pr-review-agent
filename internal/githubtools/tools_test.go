package githubtools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liaohonghui/github-pr-review-agent/internal/agent"
	"github.com/liaohonghui/github-pr-review-agent/internal/github"
)

type fakeClient struct {
	pr           *github.PullRequest
	files        []github.PullRequestFile
	commits      []github.PullRequestCommit
	fileContents map[string]string
	tarball      io.Reader

	getPRCalls    int
	getFilesCalls int
	owners        []string
	repos         []string
	numbers       []int
	commitLimits  []int
	filePaths     []string
	refs          []string
	tarballRef    string
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

func (f *fakeClient) GetRepositoryTarball(ctx context.Context, owner, repo, ref string) (io.ReadCloser, error) {
	f.tarballRef = ref
	if f.tarball == nil {
		return io.NopCloser(strings.NewReader("")), nil
	}
	return io.NopCloser(f.tarball), nil
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

func TestToolkitRegistersSixReadOnlyTools(t *testing.T) {
	toolkit := NewToolkit(newFakeClient(), "owner", "repo", 12, Options{})
	tools := toolkit.Tools()
	expected := []string{
		"get_pr_meta",
		"list_changed_files",
		"read_diff",
		"read_file_context",
		"search_references",
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

func TestToolkitRegistersStaticChecksOnlyWhenEnabled(t *testing.T) {
	defaultToolkit := NewToolkit(newFakeClient(), "owner", "repo", 12, Options{})
	if names := toolNames(defaultToolkit.Tools()); containsString(names, "run_static_checks") {
		t.Fatalf("run_static_checks should not be registered by default: %v", names)
	}

	staticToolkit := NewToolkit(newFakeClient(), "owner", "repo", 12, Options{
		EnableStaticChecks: true,
		StaticCheckWorkDir: t.TempDir(),
	})
	names := toolNames(staticToolkit.Tools())
	if len(names) != 7 || names[6] != "run_static_checks" {
		t.Fatalf("unexpected enabled tools: %v", names)
	}
}

func toolNames(tools []agent.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name())
	}
	return names
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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

func TestFileContextToolInfersChangedLinesAndUsesTreeSitter(t *testing.T) {
	client := newFakeClient()
	client.files = []github.PullRequestFile{{
		Filename: "internal/auth/auth.go",
		Status:   "modified",
		Patch:    "@@ -0,0 +3,3 @@\n+func Validate() error {\n+\treturn nil\n+}",
	}}
	client.fileContents = map[string]string{
		"internal/auth/auth.go": `package auth

func Validate() error {
	return nil
}

func Other() {}
`,
	}
	toolkit := NewToolkit(client, "owner", "repo", 12, Options{MaxFileContextLines: 20})

	output, err := toolByName(t, toolkit, "read_file_context").Execute(context.Background(), map[string]any{
		"path": "internal/auth/auth.go",
	})
	if err != nil {
		t.Fatalf("read_file_context: %v", err)
	}

	var result struct {
		ContextMode string `json:"context_mode"`
		Language    string `json:"language"`
		Content     string `json:"content"`
		Symbols     []struct {
			Name string `json:"name"`
		} `json:"symbols"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if result.ContextMode != "tree_sitter" || result.Language != "go" {
		t.Fatalf("unexpected context mode: %+v", result)
	}
	if len(result.Symbols) != 1 || result.Symbols[0].Name != "Validate" {
		t.Fatalf("unexpected symbols: %+v", result.Symbols)
	}
	if !strings.Contains(result.Content, "func Validate") || strings.Contains(result.Content, "func Other") {
		t.Fatalf("unexpected context: %s", result.Content)
	}
}

func TestChangedLinesForPathParsesUnifiedDiff(t *testing.T) {
	files := []github.PullRequestFile{{
		Filename: "service.py",
		Patch:    "@@ -2,3 +3,4 @@ context\n unchanged\n-old\n+new\n+added\n context",
	}}

	if got := changedLinesForPath(files, "service.py"); len(got) != 2 || got[0] != 4 || got[1] != 5 {
		t.Fatalf("changed lines = %v, want [4 5]", got)
	}
}

func TestSearchReferencesFindsExactIdentifierAtPullRequestHead(t *testing.T) {
	client := newFakeClient()
	client.files = []github.PullRequestFile{{
		Filename: "internal/config/config.go",
		Status:   "modified",
		Patch:    "@@ -3 +2,0 @@\n-	MaxDiffLines int",
	}}
	client.tarball = gzipTarballForTest(t, map[string]string{
		"cmd/server/main.go":        "package main\n_ = cfg.MaxDiffLines\n",
		"internal/config/config.go": "package config\n\ntype Config struct {}\n",
		"internal/other.go":         "package other\n_ = MaxDiffLinesExtra\n",
		"vendor/skip.go":            "package vendor\n_ = MaxDiffLines\n",
	})
	toolkit := NewToolkit(client, "owner", "repo", 12, Options{})

	output, err := toolByName(t, toolkit, "search_references").Execute(context.Background(), map[string]any{
		"symbol": "MaxDiffLines",
	})
	if err != nil {
		t.Fatalf("search_references: %v", err)
	}

	var result struct {
		Ref          string `json:"ref"`
		ScannedFiles int    `json:"scanned_files"`
		MatchedFiles int    `json:"matched_files"`
		Truncated    bool   `json:"truncated"`
		Matches      []struct {
			Path        string `json:"path"`
			Line        int    `json:"line"`
			Snippet     string `json:"snippet"`
			ChangedFile bool   `json:"changed_file"`
		} `json:"matches"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if client.tarballRef != "head-sha" {
		t.Fatalf("tarball ref = %s, want head-sha", client.tarballRef)
	}
	if result.ScannedFiles != 3 || result.MatchedFiles != 1 || len(result.Matches) != 1 {
		t.Fatalf("unexpected search result: %+v", result)
	}
	match := result.Matches[0]
	if match.Path != "cmd/server/main.go" || match.Line != 2 ||
		!strings.Contains(match.Snippet, "cfg.MaxDiffLines") || match.ChangedFile {
		t.Fatalf("unexpected match: %+v", match)
	}
	if result.Truncated {
		t.Fatalf("unexpected truncation: %+v", result)
	}
}

func TestStaticChecksRunServerDefinedCommandsAndCleanTemporaryRepository(t *testing.T) {
	client := newFakeClient()
	client.tarball = gzipTarballForTest(t, map[string]string{
		"go.mod":        "module example.test/repo\n\ngo 1.25\n",
		"main.go":       "package main\n\nfunc main() {}\n",
		"internal/a.go": "package internal\n",
	})
	workDir := t.TempDir()
	toolkit := NewToolkit(client, "owner", "repo", 12, Options{
		EnableStaticChecks: true,
		StaticCheckWorkDir: workDir,
		StaticCheckGoProxy: "off",
	})

	var (
		commandDirs    []string
		commandEnvs    [][]string
		extractedGoMod bool
	)
	toolkit.staticCheckRunner = func(
		ctx context.Context,
		args []string,
		dir string,
		env []string,
	) (staticCheckCommandResult, error) {
		commandDirs = append(commandDirs, dir)
		commandEnvs = append(commandEnvs, env)
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			extractedGoMod = true
		}
		return staticCheckCommandResult{Success: true, Output: "ok"}, nil
	}

	output, err := toolByName(t, toolkit, "run_static_checks").Execute(context.Background(), map[string]any{
		"checks": []any{"go_vet"},
	})
	if err != nil {
		t.Fatalf("run_static_checks: %v", err)
	}
	var result struct {
		Supported bool `json:"supported"`
		Checks    []struct {
			Name       string `json:"name"`
			Command    string `json:"command"`
			Success    bool   `json:"success"`
			DurationMS int64  `json:"duration_ms"`
			Output     string `json:"output"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if !result.Supported || len(result.Checks) != 1 || result.Checks[0].Name != "go_vet" ||
		result.Checks[0].Command != "go vet ./..." || !result.Checks[0].Success {
		t.Fatalf("unexpected static check result: %+v", result)
	}
	if client.tarballRef != "head-sha" {
		t.Fatalf("tarball ref = %s, want head-sha", client.tarballRef)
	}
	if len(commandDirs) != 1 {
		t.Fatalf("command calls = %d, want 1", len(commandDirs))
	}
	if !extractedGoMod {
		t.Fatal("go.mod was not extracted before command execution")
	}
	if !containsString(commandEnvs[0], "GOPROXY=off") ||
		containsString(commandEnvs[0], "DEEPSEEK_API_KEY=secret") {
		t.Fatalf("unexpected command environment: %v", commandEnvs[0])
	}
	if _, err := os.Stat(commandDirs[0]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary repository was not removed: %v", err)
	}
}

func TestStaticChecksReturnUnsupportedWithoutGoMod(t *testing.T) {
	client := newFakeClient()
	client.tarball = gzipTarballForTest(t, map[string]string{
		"README.md": "# not go\n",
	})
	toolkit := NewToolkit(client, "owner", "repo", 12, Options{
		EnableStaticChecks: true,
		StaticCheckWorkDir: t.TempDir(),
	})
	toolkit.staticCheckRunner = func(
		ctx context.Context,
		args []string,
		dir string,
		env []string,
	) (staticCheckCommandResult, error) {
		t.Fatal("runner must not be called without go.mod")
		return staticCheckCommandResult{}, nil
	}

	output, err := toolByName(t, toolkit, "run_static_checks").Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("run_static_checks: %v", err)
	}
	var result struct {
		Supported bool   `json:"supported"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if result.Supported || result.Reason != "go.mod not found at repository root" {
		t.Fatalf("unexpected unsupported result: %+v", result)
	}
}

func TestStaticChecksRejectNonWhitelistedCheck(t *testing.T) {
	toolkit := NewToolkit(newFakeClient(), "owner", "repo", 12, Options{
		EnableStaticChecks: true,
		StaticCheckWorkDir: t.TempDir(),
	})
	_, err := toolByName(t, toolkit, "run_static_checks").Execute(context.Background(), map[string]any{
		"checks": []any{"go_build ./... && whoami"},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported check") {
		t.Fatalf("expected unsupported check error, got %v", err)
	}
}

func TestStaticCheckArchiveExtractionRejectsUnsafeEntries(t *testing.T) {
	tests := []struct {
		name   string
		header *tar.Header
	}{
		{
			name: "path traversal",
			header: &tar.Header{
				Name:     "repo/../../escape.txt",
				Mode:     0o600,
				Size:     0,
				Typeflag: tar.TypeReg,
			},
		},
		{
			name: "symlink",
			header: &tar.Header{
				Name:     "repo/link",
				Linkname: "/tmp/target",
				Mode:     0o777,
				Typeflag: tar.TypeSymlink,
			},
		},
		{
			name: "hard link",
			header: &tar.Header{
				Name:     "repo/link",
				Linkname: "/etc/passwd",
				Mode:     0o644,
				Typeflag: tar.TypeLink,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := gzipTarballWithHeaderForTest(t, test.header)
			err := extractStaticCheckArchive(archive, t.TempDir())
			if err == nil {
				t.Fatal("expected unsafe archive error")
			}
		})
	}
}

func TestSearchReferencesCapsResultsAndFiltersPathPrefix(t *testing.T) {
	client := newFakeClient()
	client.tarball = gzipTarballForTest(t, map[string]string{
		"cmd/server/a.go": "package main\n_ = cfg.MaxDiffLines\n",
		"cmd/server/b.go": "package main\n_ = cfg.MaxDiffLines\n",
	})
	toolkit := NewToolkit(client, "owner", "repo", 12, Options{MaxReferenceResults: 1})

	output, err := toolByName(t, toolkit, "search_references").Execute(context.Background(), map[string]any{
		"symbol":      "MaxDiffLines",
		"path_prefix": "cmd/server/",
		"max_results": 10,
	})
	if err != nil {
		t.Fatalf("search_references: %v", err)
	}
	var result struct {
		ScannedFiles int `json:"scanned_files"`
		Matches      []struct {
			Path string `json:"path"`
		} `json:"matches"`
		Truncated     bool `json:"truncated"`
		MaxResultsCap bool `json:"max_results_capped"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if result.ScannedFiles != 1 || len(result.Matches) != 1 || !result.Truncated || !result.MaxResultsCap {
		t.Fatalf("unexpected capped result: %+v", result)
	}
	if result.Matches[0].Path != "cmd/server/a.go" && result.Matches[0].Path != "cmd/server/b.go" {
		t.Fatalf("unexpected match path: %+v", result.Matches[0])
	}
}

func TestSearchReferencesRejectsInvalidSymbol(t *testing.T) {
	toolkit := NewToolkit(newFakeClient(), "owner", "repo", 12, Options{})
	_, err := toolByName(t, toolkit, "search_references").Execute(context.Background(), map[string]any{
		"symbol": "cfg.MaxDiffLines",
	})
	if err == nil || !strings.Contains(err.Error(), "symbol must be") {
		t.Fatalf("expected invalid symbol error, got %v", err)
	}
}

func gzipTarballForTest(t *testing.T, files map[string]string) io.Reader {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for filePath, content := range files {
		if err := tarWriter.WriteHeader(&tar.Header{
			Name:     "repo-head/" + filePath,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := tarWriter.Write([]byte(content)); err != nil {
			t.Fatalf("write tar content: %v", err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return &buffer
}

func gzipTarballWithHeaderForTest(t *testing.T, header *tar.Header) io.Reader {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(header); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return &buffer
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
