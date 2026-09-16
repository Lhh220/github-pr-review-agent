package githubtools

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const retrievalOutputBytes = 12 << 10

type retrieveCodeTool struct{ toolkit *Toolkit }

func (retrieveCodeTool) Name() string { return "retrieve_code_context" }
func (retrieveCodeTool) Description() string {
	return "Retrieve ranked code chunks across the PR head repository using 1-8 concrete keywords. Returns exact source lines for evidence; limited results do not establish absence. Prefer search_references for a single exact identifier."
}
func (retrieveCodeTool) Schema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"query": map[string]any{"type": "string", "maxLength": 256},
		"top_k": map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
	}, "required": []string{"query"}, "additionalProperties": false}
}

type retrievalLine struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Snippet string `json:"snippet"`
}

type retrievalChunk struct {
	Path      string          `json:"path"`
	StartLine int             `json:"start_line"`
	EndLine   int             `json:"end_line"`
	Score     int             `json:"score"`
	Lines     []retrievalLine `json:"-"`
}
type retrievalOutput struct {
	Query            string           `json:"query"`
	Ref              string           `json:"ref"`
	Strategy         string           `json:"strategy"`
	ScannedFiles     int              `json:"scanned_files"`
	ScanTruncated    bool             `json:"scan_truncated"`
	ResultsTruncated bool             `json:"results_truncated"`
	Chunks           []retrievalChunk `json:"chunks"`
	Matches          []retrievalLine  `json:"matches"`
}

func (t retrieveCodeTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	query, _ := input["query"].(string)
	if len(query) > 256 {
		return "", fmt.Errorf("query exceeds 256 bytes")
	}
	terms := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' })
	unique := make(map[string]bool)
	for _, term := range terms {
		unique[term] = true
	}
	terms = terms[:0]
	for term := range unique {
		terms = append(terms, term)
	}
	sort.Strings(terms)
	if len(terms) == 0 || len(terms) > 8 {
		return "", fmt.Errorf("query must contain 1-8 distinct keywords")
	}
	k, err := optionalInt(input, "top_k", 3)
	if err != nil {
		return "", err
	}
	if k < 1 || k > 5 {
		return "", fmt.Errorf("top_k must be between 1 and 5")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	pr, err := t.toolkit.pullRequest(ctx)
	if err != nil {
		return "", err
	}
	archive, err := t.toolkit.cachedTarball(ctx, pr.Head.SHA)
	if err != nil {
		return "", err
	}
	defer archive.Close()
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	// Count skipped entries and tar metadata too, not just searchable file bodies.
	limited := &io.LimitedReader{R: gz, N: maxSearchArchiveBytes + 1}
	reader := tar.NewReader(limited)
	out := retrievalOutput{Query: query, Ref: pr.Head.SHA, Strategy: "lexical", Chunks: []retrievalChunk{}, Matches: []retrievalLine{}}
	candidates := 0
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		header, err := reader.Next()
		if limited.N <= 0 {
			out.ScanTruncated = true
			break
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read retrieval archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		name := normalizeArchivePath(header.Name)
		if !isSearchablePath(name) || name == "" || strings.HasPrefix(name, "eval/report-") {
			continue
		}
		if header.Size > maxSearchFileBytes {
			out.ScanTruncated = true
			continue
		}
		if out.ScannedFiles >= maxSearchFiles {
			out.ScanTruncated = true
			break
		}
		out.ScannedFiles++
		content, err := io.ReadAll(reader)
		if limited.N <= 0 {
			out.ScanTruncated = true
			break
		}
		if err != nil {
			return "", err
		}
		if !utf8.Valid(content) || strings.ContainsRune(string(content), 0) {
			continue
		}
		lines := strings.Split(string(content), "\n")
		// ponytail: bounded lexical scan per query; add an index only if measured scan cost warrants it.
		for start := 0; start < len(lines); start += 16 {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			end := min(start+20, len(lines))
			body := strings.ToLower(strings.Join(lines[start:end], "\n"))
			score := 0
			for _, term := range terms {
				if strings.Contains(body, term) {
					score += 4
				}
				if strings.Contains(strings.ToLower(name), term) {
					score++
				}
			}
			if score == 0 {
				continue
			}
			chunk := retrievalChunk{Path: name, StartLine: start + 1, EndLine: end, Score: score}
			for i := start; i < end; i++ {
				chunk.Lines = append(chunk.Lines, retrievalLine{Path: name, Line: i + 1, Snippet: strings.TrimSpace(lines[i])})
			}
			// Omit oversized chunks intact rather than manufacture partial source evidence.
			encoded, _ := json.Marshal(chunk.Lines)
			if len(encoded) > retrievalOutputBytes/2 {
				out.ResultsTruncated = true
				continue
			}
			candidates++
			out.Chunks = append(out.Chunks, chunk)
			sort.Slice(out.Chunks, func(i, j int) bool {
				a, b := out.Chunks[i], out.Chunks[j]
				if a.Score != b.Score {
					return a.Score > b.Score
				}
				if a.Path != b.Path {
					return a.Path < b.Path
				}
				return a.StartLine < b.StartLine
			})
			if len(out.Chunks) > k {
				out.Chunks = out.Chunks[:k]
			}
			if end == len(lines) {
				break
			}
		}
	}
	out.ResultsTruncated = out.ResultsTruncated || candidates > len(out.Chunks)
	for {
		out.Matches = []retrievalLine{}
		for _, chunk := range out.Chunks {
			out.Matches = append(out.Matches, chunk.Lines...)
		}
		encoded, err := json.Marshal(out)
		if err != nil {
			return "", err
		}
		if len(encoded) <= retrievalOutputBytes {
			return string(encoded), nil
		}
		out.ResultsTruncated = true
		out.Chunks = out.Chunks[:len(out.Chunks)-1]
	}
}
