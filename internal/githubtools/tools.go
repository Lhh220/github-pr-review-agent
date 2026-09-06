package githubtools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/liaohonghui/github-pr-review-agent/internal/agent"
	"github.com/liaohonghui/github-pr-review-agent/internal/github"
)

const (
	defaultMaxDiffLines        = 2000
	defaultMaxFileContextLines = 200
	defaultMaxCommitHistory    = 20
	maxListedFiles             = 200
	maxToolOutputChars         = 40000
	maxPRBodyChars             = 12000
)

type Client interface {
	GetPullRequest(ctx context.Context, owner, repo string, number int) (*github.PullRequest, error)
	GetPullRequestFiles(ctx context.Context, owner, repo string, number int) ([]github.PullRequestFile, error)
	GetPullRequestCommits(ctx context.Context, owner, repo string, number, limit int) ([]github.PullRequestCommit, error)
	GetFileContent(ctx context.Context, owner, repo, path, ref string) (string, error)
}

type Options struct {
	MaxDiffLines        int
	MaxFileContextLines int
	MaxCommitHistory    int
}

type Toolkit struct {
	client Client
	owner  string
	repo   string
	number int

	maxDiffLines        int
	maxFileContextLines int
	maxCommitHistory    int

	mu          sync.Mutex
	cachedPR    *github.PullRequest
	cachedFiles []github.PullRequestFile
	filesLoaded bool
}

func NewToolkit(client Client, owner, repo string, number int, options Options) *Toolkit {
	normalizePositive(&options.MaxDiffLines, defaultMaxDiffLines)
	normalizePositive(&options.MaxFileContextLines, defaultMaxFileContextLines)
	normalizePositive(&options.MaxCommitHistory, defaultMaxCommitHistory)
	if options.MaxCommitHistory > 100 {
		options.MaxCommitHistory = 100
	}

	return &Toolkit{
		client:              client,
		owner:               owner,
		repo:                repo,
		number:              number,
		maxDiffLines:        options.MaxDiffLines,
		maxFileContextLines: options.MaxFileContextLines,
		maxCommitHistory:    options.MaxCommitHistory,
	}
}

func (t *Toolkit) Tools() []agent.Tool {
	return []agent.Tool{
		prMetaTool{toolkit: t},
		changedFilesTool{toolkit: t},
		diffTool{toolkit: t},
		fileContextTool{toolkit: t},
		commitHistoryTool{toolkit: t},
	}
}

func (t *Toolkit) PullRequest(ctx context.Context) (*github.PullRequest, error) {
	return t.pullRequest(ctx)
}

func (t *Toolkit) pullRequest(ctx context.Context) (*github.PullRequest, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.cachedPR != nil {
		return t.cachedPR, nil
	}
	pr, err := t.client.GetPullRequest(ctx, t.owner, t.repo, t.number)
	if err != nil {
		return nil, fmt.Errorf("get pull request: %w", err)
	}
	t.cachedPR = pr
	return pr, nil
}

func (t *Toolkit) files(ctx context.Context) ([]github.PullRequestFile, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.filesLoaded {
		return t.cachedFiles, nil
	}
	files, err := t.client.GetPullRequestFiles(ctx, t.owner, t.repo, t.number)
	if err != nil {
		return nil, fmt.Errorf("get pull request files: %w", err)
	}
	t.cachedFiles = files
	t.filesLoaded = true
	return files, nil
}

type prMetaTool struct {
	toolkit *Toolkit
}

func (prMetaTool) Name() string { return "get_pr_meta" }

func (prMetaTool) Description() string {
	return "Get the current pull request title, description, source and target branches, and head commit."
}

func (prMetaTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
}

func (t prMetaTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	pr, err := t.toolkit.pullRequest(ctx)
	if err != nil {
		return "", err
	}
	body, bodyTruncated := clampString(pr.Body, maxPRBodyChars)
	return encodeJSON(map[string]any{
		"number":         pr.Number,
		"title":          pr.Title,
		"body":           body,
		"body_truncated": bodyTruncated,
		"head":           map[string]string{"sha": pr.Head.SHA, "ref": pr.Head.Ref},
		"base":           map[string]string{"sha": pr.Base.SHA, "ref": pr.Base.Ref},
	})
}

type changedFilesTool struct {
	toolkit *Toolkit
}

func (changedFilesTool) Name() string { return "list_changed_files" }

func (changedFilesTool) Description() string {
	return "List files changed by the current pull request with status and line-count statistics, without patches."
}

func (changedFilesTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
}

func (t changedFilesTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	files, err := t.toolkit.files(ctx)
	if err != nil {
		return "", err
	}
	listed := files
	truncated := false
	if len(listed) > maxListedFiles {
		listed = listed[:maxListedFiles]
		truncated = true
	}
	summaries := make([]map[string]any, 0, len(listed))
	for _, file := range listed {
		summaries = append(summaries, map[string]any{
			"path":      file.Filename,
			"status":    file.Status,
			"additions": file.Additions,
			"deletions": file.Deletions,
			"changes":   file.Changes,
		})
	}
	return encodeJSON(map[string]any{
		"total_files": len(files),
		"truncated":   truncated,
		"files":       summaries,
	})
}

type diffTool struct {
	toolkit *Toolkit
}

func (diffTool) Name() string { return "read_diff" }

func (diffTool) Description() string {
	return "Read the unified diff for the current pull request. Provide path to read one changed file, or omit it to read the bounded full diff."
}

func (diffTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Exact changed-file path. Omit to read the full bounded diff.",
			},
		},
		"additionalProperties": false,
	}
}

func (t diffTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	files, err := t.toolkit.files(ctx)
	if err != nil {
		return "", err
	}
	path, _ := input["path"].(string)
	if strings.TrimSpace(path) != "" {
		return fileDiff(files, strings.TrimSpace(path), t.toolkit.maxDiffLines)
	}
	return fullDiff(files, t.toolkit.maxDiffLines)
}

type fileContextTool struct {
	toolkit *Toolkit
}

func (fileContextTool) Name() string { return "read_file_context" }

func (fileContextTool) Description() string {
	return "Read a bounded line range from a file in the current repository at the pull request head commit."
}

func (fileContextTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
			"start_line": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"description": "One-based starting line. Defaults to 1.",
			},
			"end_line": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"description": "One-based inclusive ending line. The server caps the requested range.",
			},
		},
		"required":             []string{"path"},
		"additionalProperties": false,
	}
}

func (t fileContextTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	path, _ := input["path"].(string)
	path = strings.TrimSpace(path)
	if path == "" || strings.HasPrefix(path, "/") || path == "." || path == ".." || strings.Contains(path, "\x00") {
		return "", fmt.Errorf("invalid file path")
	}

	pr, err := t.toolkit.pullRequest(ctx)
	if err != nil {
		return "", err
	}
	content, err := t.toolkit.client.GetFileContent(ctx, t.toolkit.owner, t.toolkit.repo, path, pr.Head.SHA)
	if err != nil {
		return "", fmt.Errorf("get file content: %w", err)
	}

	startLine, err := optionalInt(input, "start_line", 1)
	if err != nil {
		return "", fmt.Errorf("decode start_line: %w", err)
	}
	if startLine < 1 {
		return "", fmt.Errorf("start_line must be at least 1")
	}
	defaultEnd := startLine + t.toolkit.maxFileContextLines - 1
	endLine, err := optionalInt(input, "end_line", defaultEnd)
	if err != nil {
		return "", fmt.Errorf("decode end_line: %w", err)
	}
	if endLine < startLine {
		return "", fmt.Errorf("end_line must be greater than or equal to start_line")
	}
	rangeCapped := false
	if endLine-startLine+1 > t.toolkit.maxFileContextLines {
		endLine = startLine + t.toolkit.maxFileContextLines - 1
		rangeCapped = true
	}

	lines := splitLines(content)
	selected := make([]string, 0, t.toolkit.maxFileContextLines)
	for lineNumber := startLine; lineNumber <= endLine && lineNumber <= len(lines); lineNumber++ {
		selected = append(selected, fmt.Sprintf("%d: %s", lineNumber, lines[lineNumber-1]))
	}
	context, contextTruncated := clampString(strings.Join(selected, "\n"), maxToolOutputChars)
	if len(selected) == 0 {
		context = fmt.Sprintf("The file has %d lines; requested start_line %d is beyond the end.", len(lines), startLine)
	}
	return encodeJSON(map[string]any{
		"path":              path,
		"ref":               pr.Head.SHA,
		"start_line":        startLine,
		"end_line":          min(endLine, len(lines)),
		"total_lines":       len(lines),
		"range_capped":      rangeCapped,
		"content_truncated": contextTruncated,
		"content":           context,
	})
}

type commitHistoryTool struct {
	toolkit *Toolkit
}

func (commitHistoryTool) Name() string { return "get_commit_history" }

func (commitHistoryTool) Description() string {
	return "Get recent commits in the current pull request to understand the change motivation."
}

func (commitHistoryTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"limit": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"description": "Maximum commits to return. The server caps this value.",
			},
		},
		"additionalProperties": false,
	}
}

func (t commitHistoryTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	limit, err := optionalInt(input, "limit", t.toolkit.maxCommitHistory)
	if err != nil {
		return "", fmt.Errorf("decode limit: %w", err)
	}
	if limit < 1 {
		return "", fmt.Errorf("limit must be at least 1")
	}
	limitCapped := false
	if limit > t.toolkit.maxCommitHistory {
		limit = t.toolkit.maxCommitHistory
		limitCapped = true
	}
	commits, err := t.toolkit.client.GetPullRequestCommits(ctx, t.toolkit.owner, t.toolkit.repo, t.toolkit.number, limit)
	if err != nil {
		return "", fmt.Errorf("get pull request commits: %w", err)
	}
	items := make([]map[string]any, 0, len(commits))
	for _, commit := range commits {
		items = append(items, map[string]any{
			"sha":         commit.SHA,
			"message":     commit.Commit.Message,
			"author_name": commit.Commit.Author.Name,
			"author_date": commit.Commit.Author.Date,
		})
	}
	return encodeJSON(map[string]any{
		"limit":        limit,
		"limit_capped": limitCapped,
		"commits":      items,
	})
}

func fileDiff(files []github.PullRequestFile, path string, maxLines int) (string, error) {
	for _, file := range files {
		if file.Filename != path {
			continue
		}
		patch, lines, truncated := takeLines(file.Patch, maxLines)
		return encodeJSON(map[string]any{
			"path":             file.Filename,
			"status":           file.Status,
			"additions":        file.Additions,
			"deletions":        file.Deletions,
			"found":            true,
			"patch_lines_used": lines,
			"truncated":        truncated,
			"patch":            patch,
		})
	}
	return encodeJSON(map[string]any{"path": path, "found": false})
}

func fullDiff(files []github.PullRequestFile, maxLines int) (string, error) {
	remaining := maxLines
	patches := make([]map[string]any, 0, len(files))
	truncated := false
	for _, file := range files {
		if remaining <= 0 {
			truncated = true
			break
		}
		patch, used, patchTruncated := takeLines(file.Patch, remaining)
		if used == 0 && patchTruncated {
			truncated = true
			break
		}
		patches = append(patches, map[string]any{
			"path":      file.Filename,
			"status":    file.Status,
			"patch":     patch,
			"truncated": patchTruncated,
		})
		remaining -= used
		if patchTruncated {
			truncated = true
			break
		}
	}
	return encodeJSON(map[string]any{
		"total_files": len(files),
		"max_lines":   maxLines,
		"truncated":   truncated,
		"files":       patches,
	})
}

func encodeJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode tool output: %w", err)
	}
	if len(encoded) <= maxToolOutputChars {
		return string(encoded), nil
	}
	encoded, err = json.Marshal(map[string]any{
		"truncated": true,
		"reason":    "tool output exceeded the safety limit; request a narrower path, line range, or diff",
	})
	if err != nil {
		return "", fmt.Errorf("encode truncated tool output: %w", err)
	}
	return string(encoded), nil
}

func splitLines(content string) []string {
	content = strings.TrimSuffix(content, "\n")
	if content == "" {
		return nil
	}
	return strings.Split(content, "\n")
}

func takeLines(content string, limit int) (string, int, bool) {
	if content == "" {
		return "", 0, false
	}
	lines := splitLines(content)
	if limit <= 0 || len(lines) <= limit {
		return content, len(lines), false
	}
	return strings.Join(lines[:limit], "\n") + "\n[patch truncated by tool limit]", limit, true
}

func clampString(value string, limit int) (string, bool) {
	if limit <= 0 || len(value) <= limit {
		return value, false
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false
	}
	return string(runes[:limit]) + "\n[truncated by tool limit]", true
}

func optionalInt(input map[string]any, key string, fallback int) (int, error) {
	value, exists := input[key]
	if !exists || value == nil {
		return fallback, nil
	}
	switch typed := value.(type) {
	case float64:
		if typed != float64(int(typed)) {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		return int(typed), nil
	case int:
		return typed, nil
	default:
		return 0, fmt.Errorf("%s must be an integer", key)
	}
}

func normalizePositive(value *int, fallback int) {
	if *value <= 0 {
		*value = fallback
	}
}
