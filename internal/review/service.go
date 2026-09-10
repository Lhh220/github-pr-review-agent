package review

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/liaohonghui/github-pr-review-agent/internal/github"
	"github.com/liaohonghui/github-pr-review-agent/internal/llm"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

type GitHubClient interface {
	ReviewPublisher
	GetPullRequest(ctx context.Context, owner, repo string, number int) (*github.PullRequest, error)
	GetPullRequestFiles(ctx context.Context, owner, repo string, number int) ([]github.PullRequestFile, error)
	GetFileContent(ctx context.Context, owner, repo, path, ref string) (string, error)
	CreatePullRequestReview(ctx context.Context, owner, repo string, number int, body string) error
}

type LLMClient interface {
	ReviewCode(ctx context.Context, title, body, diff, fileContext string) (llm.ReviewResponse, error)
}

type ResultStore interface {
	GetReviewResultByTaskID(context.Context, uint64) (*store.ReviewResult, error)
	GetReviewDelivery(context.Context, uint64) (*store.ReviewDelivery, error)
	PrepareReviewDelivery(context.Context, uint64, string) (*store.ReviewDelivery, error)
	MarkReviewDelivered(context.Context, uint64, uint64) error
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
	pr, completed, err := resumeReview(ctx, s.GitHub, s.Results, owner, repo, number, taskID)
	if err != nil || completed {
		return err
	}
	files, err := s.GitHub.GetPullRequestFiles(ctx, owner, repo, number)
	if err != nil {
		return fmt.Errorf("get pull request files: %w", err)
	}
	if len(files) == 0 || isDocsOnlyPR(files) {
		summary := "This pull request has no changed files relative to its base branch; review skipped."
		rawResponse := "No changed files relative to the base branch."
		if len(files) > 0 {
			summary = "This pull request only changes documentation; code review skipped."
			rawResponse = "Documentation-only pull request; code review skipped."
		}
		return finishReview(ctx, s.GitHub, s.Results, owner, repo, number, store.NewReviewResult{
			TaskID:      taskID,
			Summary:     summary,
			Findings:    []store.Finding{},
			RawResponse: rawResponse,
			Model:       "none",
		})
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
	parsed, err := parseReviewResponse(response.Content, diff, fileContext)
	if err != nil {
		return err
	}
	return finishReview(ctx, s.GitHub, s.Results, owner, repo, number, store.NewReviewResult{
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
}

type parsedReviewResponse struct {
	Summary  string          `json:"summary"`
	Findings []store.Finding `json:"findings"`
}

func parseReviewResponse(content string, evidenceCorpus ...string) (parsedReviewResponse, error) {
	cleaned := extractJSONObject(content)
	var parsed parsedReviewResponse
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		return parsedReviewResponse{}, fmt.Errorf("parse review response: %w", err)
	}
	if strings.TrimSpace(parsed.Summary) == "" || parsed.Findings == nil {
		return parsedReviewResponse{}, fmt.Errorf("parse review response: summary and findings array are required")
	}
	parsed.Findings = normalizeFindings(parsed.Findings, evidenceCorpus...)
	return parsed, nil
}

func normalizeFindings(findings []store.Finding, evidenceCorpus ...string) []store.Finding {
	normalized := make([]store.Finding, 0, len(findings))
	corpusStrings := evidenceStrings(evidenceCorpus)
	for _, finding := range findings {
		evidence := supportedEvidence(finding, evidenceCorpus, corpusStrings)
		if finding.File == "" || finding.Line <= 0 || len(evidence) == 0 {
			continue
		}
		if finding.Confidence != "confirmed" || finding.Category == "performance" {
			finding.Confidence = "needs_verification"
		}
		finding.Evidence = evidence
		normalized = append(normalized, finding)
	}
	return normalized
}

func supportedEvidence(finding store.Finding, corpus, corpusStrings []string) []store.Evidence {
	supported := make([]store.Evidence, 0, len(finding.Evidence))
	for _, evidence := range finding.Evidence {
		switch evidence.Type {
		case "reference":
			if strings.TrimSpace(evidence.Text) == "" || evidence.File != finding.File || evidence.Line != finding.Line ||
				!referenceEvidenceInCorpus(evidence, corpus) {
				continue
			}
		case "static_check":
			if evidence.Command == "" || !containsString(corpusStrings, evidence.Command) ||
				!staticCheckEvidenceInCorpus(evidence, corpus) {
				continue
			}
		default:
			continue
		}
		supported = append(supported, evidence)
	}
	return supported
}

func referenceEvidenceInCorpus(evidence store.Evidence, corpus []string) bool {
	for _, source := range corpus {
		var output struct {
			Path    string `json:"path"`
			Content string `json:"content"`
			Matches []struct {
				Path    string `json:"path"`
				Line    int    `json:"line"`
				Snippet string `json:"snippet"`
			} `json:"matches"`
		}
		if json.Unmarshal([]byte(source), &output) == nil {
			if output.Path == evidence.File {
				prefix := fmt.Sprintf("%d:", evidence.Line)
				target := strings.TrimSpace(evidence.Text)
				for _, contentLine := range strings.Split(output.Content, "\n") {
					contentLine = strings.TrimSpace(contentLine)
					code := strings.TrimSpace(strings.TrimPrefix(contentLine, prefix))
					if strings.HasPrefix(contentLine, prefix) && code == target {
						return true
					}
				}
			}
			for _, match := range output.Matches {
				if match.Path == evidence.File && match.Line == evidence.Line &&
					match.Snippet == strings.TrimSpace(evidence.Text) {
					return true
				}
			}
			continue
		}
		if rawReferenceEvidence(source, evidence) {
			return true
		}
	}
	return false
}

func rawReferenceEvidence(source string, evidence store.Evidence) bool {
	for path, section := range rawSections(source) {
		if path != evidence.File {
			continue
		}
		if isUnifiedDiff(section) {
			if diffReferenceEvidence(section, evidence) {
				return true
			}
			continue
		}
		if plainReferenceEvidence(section, evidence) {
			return true
		}
	}
	return false
}

func rawSections(source string) map[string]string {
	sections := map[string]string{}
	var path string
	var section []string
	flush := func() {
		if path != "" {
			sections[path] = strings.Join(section, "\n")
		}
	}
	for _, line := range strings.Split(source, "\n") {
		if strings.HasPrefix(line, "### ") {
			flush()
			path = strings.TrimSpace(strings.TrimPrefix(line, "### "))
			section = nil
			continue
		}
		if path != "" {
			section = append(section, line)
		}
	}
	flush()
	return sections
}

func isUnifiedDiff(section string) bool {
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "@@ ") {
			return true
		}
	}
	return false
}

func diffReferenceEvidence(section string, evidence store.Evidence) bool {
	target := strings.TrimSpace(evidence.Text)
	newLineNumber := 0
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "@@ ") {
			newLineNumber = parseDiffHunkStart(line)
			continue
		}
		if newLineNumber <= 0 || line == "" ||
			strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") ||
			strings.HasPrefix(line, "\\") {
			continue
		}
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, " ") {
			if newLineNumber == evidence.Line && strings.TrimSpace(line[1:]) == target {
				return true
			}
			newLineNumber++
		}
	}
	return false
}

func parseDiffHunkStart(header string) int {
	plusIndex := strings.Index(header, "+")
	if plusIndex < 0 {
		return 0
	}
	value := header[plusIndex+1:]
	if endIndex := strings.IndexAny(value, ", "); endIndex >= 0 {
		value = value[:endIndex]
	}
	number, err := strconv.Atoi(value)
	if err != nil || number <= 0 {
		return 0
	}
	return number
}

func plainReferenceEvidence(section string, evidence store.Evidence) bool {
	lines := strings.Split(section, "\n")
	if evidence.Line <= 0 || evidence.Line > len(lines) {
		return false
	}
	return strings.TrimSpace(lines[evidence.Line-1]) == strings.TrimSpace(evidence.Text)
}

func staticCheckEvidenceInCorpus(evidence store.Evidence, corpus []string) bool {
	excerpt := strings.TrimSpace(evidence.Excerpt)
	if excerpt == "" {
		return false
	}
	for _, source := range corpus {
		var output struct {
			Checks []struct {
				Command  string `json:"command"`
				Output   string `json:"output"`
				Error    string `json:"error"`
				Success  *bool  `json:"success"`
				ExitCode int    `json:"exit_code"`
				TimedOut bool   `json:"timed_out"`
			} `json:"checks"`
		}
		if json.Unmarshal([]byte(source), &output) != nil {
			continue
		}
		for _, check := range output.Checks {
			if check.Command != evidence.Command || check.Success == nil || *check.Success ||
				check.ExitCode <= 0 || check.TimedOut || check.Error != "" {
				continue
			}
			if strings.Contains(check.Output, excerpt) {
				return true
			}
		}
	}
	return false
}

func evidenceStrings(corpus []string) []string {
	values := make([]string, 0, len(corpus))
	for _, source := range corpus {
		var decoded any
		if err := json.Unmarshal([]byte(source), &decoded); err == nil {
			values = append(values, jsonStrings(decoded)...)
			continue
		}
		values = append(values, source)
	}
	return values
}

func jsonStrings(value any) []string {
	var values []string
	switch typed := value.(type) {
	case string:
		values = append(values, typed)
	case []any:
		for _, item := range typed {
			values = append(values, jsonStrings(item)...)
		}
	case map[string]any:
		for _, item := range typed {
			values = append(values, jsonStrings(item)...)
		}
	}
	return values
}

func containsString(values []string, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, value := range values {
		if strings.Contains(value, target) {
			return true
		}
	}
	return false
}

func isDocsOnlyPR(files []github.PullRequestFile) bool {
	if len(files) == 0 {
		return false
	}
	for _, file := range files {
		if !isDocumentationFile(file.Filename) {
			return false
		}
	}
	return true
}

func isDocumentationFile(filename string) bool {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".md", ".mdx", ".rst", ".adoc", ".txt":
		return true
	default:
		return false
	}
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
				"\n%d. [%s / %s / %s] `%s:%d` - %s\n",
				i+1,
				finding.Category,
				finding.Severity,
				finding.Confidence,
				finding.File,
				finding.Line,
				finding.Comment,
			))
			if finding.Suggestion != "" {
				b.WriteString(fmt.Sprintf("   Suggestion: %s\n", finding.Suggestion))
			}
			for _, evidence := range finding.Evidence {
				b.WriteString(fmt.Sprintf("   Evidence: %s\n", formatEvidence(evidence)))
			}
		}
	} else {
		b.WriteString("\n\nNo issues found.")
	}
	b.WriteString(fmt.Sprintf("\n\n---\n%s", footer))
	return b.String()
}

func formatEvidence(evidence store.Evidence) string {
	if evidence.Command != "" {
		return fmt.Sprintf("`%s`: %s", evidence.Command, evidence.Excerpt)
	}
	if evidence.File != "" {
		return fmt.Sprintf("`%s:%d`: %s", evidence.File, evidence.Line, evidence.Text)
	}
	return fmt.Sprintf("%s: %s", evidence.Type, evidence.Excerpt)
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
