package eval

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"time"

	"github.com/liaohonghui/github-pr-review-agent/internal/github"
	"github.com/liaohonghui/github-pr-review-agent/internal/llm"
	"github.com/liaohonghui/github-pr-review-agent/internal/review"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

type RunnerOptions struct {
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

type fixtureGitHubClient struct {
	fixture Fixture
}

func (c *fixtureGitHubClient) GetPullRequest(
	ctx context.Context,
	owner string,
	repo string,
	number int,
) (*github.PullRequest, error) {
	return &github.PullRequest{
		Number: number,
		Title:  c.fixture.Title,
		Body:   c.fixture.Body,
		Head:   github.Ref{SHA: c.fixture.HeadSHA, Ref: "eval-head"},
		Base:   github.Ref{SHA: "0000000000000000000000000000000000000000", Ref: "main"},
	}, nil
}

func (c *fixtureGitHubClient) GetPullRequestFiles(
	ctx context.Context,
	owner string,
	repo string,
	number int,
) ([]github.PullRequestFile, error) {
	files := make([]github.PullRequestFile, len(c.fixture.Files))
	for index, file := range c.fixture.Files {
		files[index] = github.PullRequestFile{
			Filename:  file.Filename,
			Status:    file.Status,
			Additions: file.Additions,
			Deletions: file.Deletions,
			Changes:   file.Changes,
			Patch:     file.Patch,
		}
	}
	return files, nil
}

func (c *fixtureGitHubClient) GetPullRequestCommits(
	ctx context.Context,
	owner string,
	repo string,
	number int,
	limit int,
) ([]github.PullRequestCommit, error) {
	return []github.PullRequestCommit{{
		SHA: c.fixture.HeadSHA,
		Commit: github.CommitDetail{
			Message: "eval fixture commit",
			Author: github.CommitAuthor{
				Name: "eval",
				Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			},
		},
	}}, nil
}

func (c *fixtureGitHubClient) GetFileContent(
	ctx context.Context,
	owner string,
	repo string,
	filePath string,
	ref string,
) (string, error) {
	if content, exists := c.fixture.FileContents[filePath]; exists {
		return content, nil
	}
	if content, exists := c.fixture.Repository[filePath]; exists {
		return content, nil
	}
	return "", fmt.Errorf("fixture file %q not found", filePath)
}

func (c *fixtureGitHubClient) GetRepositoryTarball(
	ctx context.Context,
	owner string,
	repo string,
	ref string,
) (io.ReadCloser, error) {
	files := map[string]string{}
	for filePath, content := range c.fixture.Repository {
		files[filePath] = content
	}
	for filePath, content := range c.fixture.FileContents {
		files[filePath] = content
	}

	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	paths := make([]string, 0, len(files))
	for filePath := range files {
		paths = append(paths, filePath)
	}
	sort.Strings(paths)
	for _, filePath := range paths {
		content := files[filePath]
		header := &tar.Header{
			Name:     path.Join("fixture-repo", filePath),
			Mode:     0o600,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return nil, fmt.Errorf("write fixture tar header: %w", err)
		}
		if _, err := tarWriter.Write([]byte(content)); err != nil {
			return nil, fmt.Errorf("write fixture tar content: %w", err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		return nil, fmt.Errorf("close fixture tar: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, fmt.Errorf("close fixture gzip: %w", err)
	}
	return io.NopCloser(bytes.NewReader(buffer.Bytes())), nil
}

func (c *fixtureGitHubClient) CreatePullRequestReview(
	ctx context.Context,
	owner string,
	repo string,
	number int,
	body string,
) error {
	return nil
}

type scriptProvider struct {
	responses []llm.ChatResponse
}

func NewScriptProvider(evaluationCase Case) *scriptProvider {
	responses := make([]llm.ChatResponse, len(evaluationCase.Script))
	for index, response := range evaluationCase.Script {
		toolCalls := make([]llm.ToolCall, len(response.ToolCalls))
		for callIndex, call := range response.ToolCalls {
			toolCalls[callIndex] = llm.ToolCall{
				ID:        call.ID,
				Name:      call.Name,
				Arguments: call.Arguments,
			}
		}
		responses[index] = llm.ChatResponse{
			Content:   response.Content,
			ToolCalls: toolCalls,
			Model:     response.Model,
			Usage: llm.Usage{
				InputTokens:  response.InputTokens,
				OutputTokens: response.OutputTokens,
				TotalTokens:  response.TotalTokens,
			},
			DurationMS: response.DurationMS,
		}
	}
	return &scriptProvider{responses: responses}
}

func (p *scriptProvider) ChatWithTools(
	ctx context.Context,
	request llm.ChatRequest,
) (llm.ChatResponse, error) {
	if len(p.responses) == 0 {
		return llm.ChatResponse{}, fmt.Errorf("eval script exhausted")
	}
	response := p.responses[0]
	p.responses = p.responses[1:]
	return response, nil
}

type memoryStore struct {
	result    *store.ReviewResult
	toolCalls []store.ToolCallLog
}

func (s *memoryStore) CreateReviewResult(
	ctx context.Context,
	input store.NewReviewResult,
) (*store.ReviewResult, error) {
	s.result = &store.ReviewResult{
		ID:            1,
		TaskID:        input.TaskID,
		Summary:       input.Summary,
		Findings:      input.Findings,
		RawResponse:   input.RawResponse,
		Model:         input.Model,
		InputTokens:   input.InputTokens,
		OutputTokens:  input.OutputTokens,
		TotalTokens:   input.TotalTokens,
		LLMDurationMS: input.LLMDurationMS,
		CreatedAt:     time.Now(),
	}
	return s.result, nil
}

func (s *memoryStore) CreateToolCallLog(
	ctx context.Context,
	input store.NewToolCallLog,
) (*store.ToolCallLog, error) {
	s.toolCalls = append(s.toolCalls, store.ToolCallLog{
		ID:         uint64(len(s.toolCalls) + 1),
		TaskID:     input.TaskID,
		ToolName:   input.ToolName,
		Output:     input.Output,
		Error:      input.Error,
		DurationMS: input.DurationMS,
		CreatedAt:  time.Now(),
	})
	return &s.toolCalls[len(s.toolCalls)-1], nil
}

func RunCase(
	ctx context.Context,
	evaluationCase Case,
	provider review.AgentProvider,
	options RunnerOptions,
) (RunResult, error) {
	gh := &fixtureGitHubClient{fixture: evaluationCase.Fixture}
	resultStore := &memoryStore{}
	service := review.NewAgent(gh, provider, resultStore, review.AgentOptions{
		MaxSteps:            options.MaxSteps,
		ToolTimeout:         options.ToolTimeout,
		MaxDiffLines:        options.MaxDiffLines,
		MaxFileContextLines: options.MaxFileContextLines,
		MaxCommitHistory:    options.MaxCommitHistory,
		MaxReferenceResults: options.MaxReferenceResults,
		EnableStaticChecks:  options.EnableStaticChecks,
		StaticCheckTimeout:  options.StaticCheckTimeout,
		StaticCheckWorkDir:  options.StaticCheckWorkDir,
		StaticCheckGoProxy:  options.StaticCheckGoProxy,
	})

	started := time.Now()
	if err := service.ReviewPR(ctx, "eval-owner", "eval-repo", 12, 1); err != nil {
		return RunResult{}, fmt.Errorf("run case %s: %w", evaluationCase.Name, err)
	}
	if resultStore.result == nil {
		return RunResult{}, fmt.Errorf("case %s produced no review result", evaluationCase.Name)
	}

	findings := resultStore.result.Findings
	if findings == nil {
		findings = []store.Finding{}
	}
	tools := make([]ToolTrace, len(resultStore.toolCalls))
	for index, call := range resultStore.toolCalls {
		tools[index] = ToolTrace{Name: call.ToolName, DurationMS: call.DurationMS}
	}
	skippedReason := ""
	if resultStore.result.Model == "none" {
		skippedReason = resultStore.result.Summary
	}
	latencyMS := time.Since(started).Milliseconds()
	if resultStore.result.LLMDurationMS > latencyMS {
		latencyMS = resultStore.result.LLMDurationMS
	}
	return RunResult{
		Findings:      findings,
		InputTokens:   resultStore.result.InputTokens,
		OutputTokens:  resultStore.result.OutputTokens,
		TotalTokens:   resultStore.result.TotalTokens,
		LatencyMS:     latencyMS,
		Tools:         tools,
		SkippedReason: skippedReason,
	}, nil
}

func WriteReport(filePath string, report Report) error {
	content, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode eval report: %w", err)
	}
	content = append(content, '\n')
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return fmt.Errorf("create eval report directory: %w", err)
	}
	if err := os.WriteFile(filePath, content, 0o644); err != nil {
		return fmt.Errorf("write eval report: %w", err)
	}
	return nil
}
