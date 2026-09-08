package githubtools

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxSearchFiles          = 2000
	maxSearchArchiveBytes   = 128 << 20
	maxSearchFileBytes      = 2 << 20
	maxReferenceSnippetSize = 500
)

type searchReferencesTool struct {
	toolkit *Toolkit
}

func (searchReferencesTool) Name() string { return "search_references" }

func (searchReferencesTool) Description() string {
	return "Search exact identifier references across the repository at the pull request head commit. Use it when a symbol, field, method, type, or config key is removed, renamed, or has unclear impact."
}

func (searchReferencesTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"symbol": map[string]any{
				"type":        "string",
				"minLength":   1,
				"maxLength":   128,
				"description": "Exact identifier such as MaxDiffLines or ValidateToken. Do not pass natural-language sentences.",
			},
			"path_prefix": map[string]any{
				"type":        "string",
				"description": "Optional repository-relative path prefix, such as internal/ or cmd/server/.",
			},
			"max_results": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"description": "Optional maximum number of references. The server caps this value.",
			},
		},
		"required":             []string{"symbol"},
		"additionalProperties": false,
	}
}

type referenceMatch struct {
	Path        string `json:"path"`
	Line        int    `json:"line"`
	Snippet     string `json:"snippet"`
	ChangedFile bool   `json:"changed_file"`
}

func (t searchReferencesTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	symbol, _ := input["symbol"].(string)
	symbol = strings.TrimSpace(symbol)
	if !isValidIdentifier(symbol) {
		return "", fmt.Errorf("symbol must be a non-empty identifier")
	}

	pathPrefix, _ := input["path_prefix"].(string)
	pathPrefix = strings.TrimSpace(pathPrefix)
	if !isSafeRelativePrefix(pathPrefix) {
		return "", fmt.Errorf("invalid path_prefix")
	}
	pathPrefix = strings.TrimSuffix(pathPrefix, "/")

	maxResults, err := optionalInt(input, "max_results", t.toolkit.maxReferenceResults)
	if err != nil {
		return "", fmt.Errorf("decode max_results: %w", err)
	}
	if maxResults < 1 {
		return "", fmt.Errorf("max_results must be at least 1")
	}
	resultCapped := false
	if maxResults > t.toolkit.maxReferenceResults {
		maxResults = t.toolkit.maxReferenceResults
		resultCapped = true
	}

	pr, err := t.toolkit.pullRequest(ctx)
	if err != nil {
		return "", err
	}
	archive, err := t.toolkit.client.GetRepositoryTarball(
		ctx,
		t.toolkit.owner,
		t.toolkit.repo,
		pr.Head.SHA,
	)
	if err != nil {
		return "", fmt.Errorf("get repository tarball: %w", err)
	}
	defer archive.Close()

	gzipReader, err := gzip.NewReader(archive)
	if err != nil {
		return "", fmt.Errorf("open repository gzip archive: %w", err)
	}
	defer gzipReader.Close()

	files, err := t.toolkit.files(ctx)
	if err != nil {
		return "", err
	}
	changedFiles := make(map[string]bool, len(files))
	for _, file := range files {
		changedFiles[file.Filename] = true
	}

	var (
		matches          []referenceMatch
		matchedFiles     = make(map[string]bool)
		scannedFiles     int
		skippedLarge     int
		archiveBytes     int64
		truncated        bool
		archiveTruncated bool
	)
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			truncated = true
			archiveTruncated = true
			break
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}

		archiveBytes += header.Size
		if archiveBytes > maxSearchArchiveBytes {
			truncated = true
			archiveTruncated = true
			break
		}

		filePath := normalizeArchivePath(header.Name)
		if filePath == "" || !isSearchablePath(filePath) {
			continue
		}
		if pathPrefix != "" && !strings.HasPrefix(filePath+"/", pathPrefix+"/") {
			continue
		}
		if header.Size > maxSearchFileBytes {
			skippedLarge++
			truncated = true
			continue
		}
		if scannedFiles >= maxSearchFiles {
			truncated = true
			archiveTruncated = true
			break
		}

		content, readErr := io.ReadAll(io.LimitReader(tarReader, maxSearchFileBytes+1))
		if readErr != nil {
			truncated = true
			archiveTruncated = true
			break
		}
		if int64(len(content)) > maxSearchFileBytes {
			skippedLarge++
			truncated = true
			continue
		}
		scannedFiles++

		for lineNumber, line := range strings.Split(string(content), "\n") {
			line = strings.TrimSuffix(line, "\r")
			if !containsIdentifier(line, symbol) {
				continue
			}
			matches = append(matches, referenceMatch{
				Path:        filePath,
				Line:        lineNumber + 1,
				Snippet:     clampSnippet(line, maxReferenceSnippetSize),
				ChangedFile: changedFiles[filePath],
			})
			matchedFiles[filePath] = true
			if len(matches) >= maxResults {
				truncated = true
				break
			}
		}
		if len(matches) >= maxResults {
			break
		}
	}

	return encodeJSON(map[string]any{
		"symbol":              symbol,
		"ref":                 pr.Head.SHA,
		"path_prefix":         pathPrefix,
		"scanned_files":       scannedFiles,
		"matched_files":       len(matchedFiles),
		"matches":             matches,
		"truncated":           truncated,
		"archive_truncated":   archiveTruncated,
		"skipped_large_files": skippedLarge,
		"max_results_capped":  resultCapped,
	})
}

func isValidIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, char := range value {
		if char == '_' || unicode.IsLetter(char) {
			continue
		}
		if index > 0 && unicode.IsDigit(char) {
			continue
		}
		return false
	}
	return true
}

func isSafeRelativePrefix(value string) bool {
	if value == "" {
		return true
	}
	if strings.ContainsRune(value, 0) || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "\\") {
		return false
	}
	cleaned := path.Clean(strings.ReplaceAll(value, "\\", "/"))
	return cleaned == "." || (!path.IsAbs(cleaned) && !strings.HasPrefix(cleaned, "../") && cleaned != "..")
}

func normalizeArchivePath(value string) string {
	normalized := strings.ReplaceAll(value, "\\", "/")
	segments := strings.Split(normalized, "/")
	if len(segments) > 1 {
		segments = segments[1:]
	}
	normalized = path.Join(segments...)
	if normalized == "." || normalized == "" || path.IsAbs(normalized) || strings.HasPrefix(normalized, "../") {
		return ""
	}
	return normalized
}

func isSearchablePath(filePath string) bool {
	lower := strings.ToLower(filePath)
	if strings.HasPrefix(lower, "vendor/") || strings.Contains(lower, "/vendor/") ||
		strings.HasPrefix(lower, "node_modules/") || strings.Contains(lower, "/node_modules/") ||
		strings.HasPrefix(lower, ".git/") {
		return false
	}

	switch path.Ext(lower) {
	case ".go", ".py", ".pyi", ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx",
		".java", ".kt", ".rs", ".rb", ".php", ".c", ".h", ".cc", ".cpp", ".hpp",
		".cs", ".sql", ".yaml", ".yml", ".json", ".toml", ".xml", ".sh", ".md":
		return true
	default:
		return false
	}
}

func containsIdentifier(line, symbol string) bool {
	offset := 0
	for offset < len(line) {
		index := strings.Index(line[offset:], symbol)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(symbol)
		if identifierBoundary(line, start, end) {
			return true
		}
		offset = start + 1
	}
	return false
}

func identifierBoundary(line string, start, end int) bool {
	if start > 0 {
		char, _ := utf8.DecodeLastRuneInString(line[:start])
		if isIdentifierRune(char) {
			return false
		}
	}
	if end < len(line) {
		char, _ := utf8.DecodeRuneInString(line[end:])
		if isIdentifierRune(char) {
			return false
		}
	}
	return true
}

func isIdentifierRune(char rune) bool {
	return char == '_' || unicode.IsLetter(char) || unicode.IsDigit(char)
}

func clampSnippet(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + " ..."
}
