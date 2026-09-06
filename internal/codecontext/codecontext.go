package codecontext

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unsafe"

	treesitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tree_sitter_javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tree_sitter_python "github.com/tree-sitter/tree-sitter-python/bindings/go"
)

const maxSymbols = 8

type Request struct {
	Path        string
	Content     string
	TargetLines []int
	MaxLines    int
}

type Symbol struct {
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	StartLine    int    `json:"start_line"`
	EndLine      int    `json:"end_line"`
	SnippetStart int    `json:"snippet_start"`
	SnippetEnd   int    `json:"snippet_end"`
	Truncated    bool   `json:"truncated"`
}

type Result struct {
	Strategy    string   `json:"strategy"`
	Language    string   `json:"language"`
	TargetLines []int    `json:"target_lines,omitempty"`
	Symbols     []Symbol `json:"symbols,omitempty"`
	Content     string   `json:"content"`
	Truncated   bool     `json:"truncated"`
}

var definitionKinds = map[string]map[string]bool{
	"go": {
		"function_declaration": true,
		"method_declaration":   true,
		"type_declaration":     true,
	},
	"python": {
		"function_definition": true,
		"class_definition":    true,
	},
	"javascript": {
		"function_declaration":           true,
		"generator_function_declaration": true,
		"method_definition":              true,
		"class_declaration":              true,
		"lexical_declaration":            true,
		"variable_declaration":           true,
	},
}

func Extract(request Request) Result {
	targetLines := normalizeLines(request.TargetLines)
	result := Result{
		Strategy:    "line_fallback",
		Language:    languageForPath(request.Path),
		TargetLines: targetLines,
	}
	if request.MaxLines <= 0 {
		request.MaxLines = 200
	}
	lines := splitLines(request.Content)
	if len(lines) == 0 || len(targetLines) == 0 {
		result.Content = ""
		return result
	}

	language, supported := parserLanguage(result.Language)
	if !supported {
		result.Content = fallbackContent(lines, targetLines, request.MaxLines)
		result.Truncated = strings.Contains(result.Content, "[context truncated")
		return result
	}

	parser := treesitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(treesitter.NewLanguage(language())); err != nil {
		result.Content = fallbackContent(lines, targetLines, request.MaxLines)
		result.Truncated = strings.Contains(result.Content, "[context truncated")
		return result
	}

	tree := parser.Parse([]byte(request.Content), nil)
	if tree == nil {
		result.Content = fallbackContent(lines, targetLines, request.MaxLines)
		result.Truncated = strings.Contains(result.Content, "[context truncated")
		return result
	}
	defer tree.Close()

	root := tree.RootNode()
	symbols := findSymbols(root, []byte(request.Content), result.Language, targetLines, lines)
	if len(symbols) == 0 {
		result.Content = fallbackContent(lines, targetLines, request.MaxLines)
		result.Truncated = strings.Contains(result.Content, "[context truncated")
		return result
	}

	result.Strategy = "tree_sitter"
	result.Symbols = symbols
	result.Content, result.Truncated = buildSymbolContent(lines, symbols, targetLines, request.MaxLines)
	return result
}

func languageForPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".py", ".pyi":
		return "python"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	default:
		return "unsupported"
	}
}

func parserLanguage(language string) (func() unsafe.Pointer, bool) {
	switch language {
	case "go":
		return tree_sitter_go.Language, true
	case "python":
		return tree_sitter_python.Language, true
	case "javascript":
		return tree_sitter_javascript.Language, true
	default:
		return nil, false
	}
}

func findSymbols(
	root *treesitter.Node,
	source []byte,
	language string,
	targetLines []int,
	lines []string,
) []Symbol {
	allowed := definitionKinds[language]
	offsets := lineOffsets(source)
	seen := make(map[uintptr]bool)
	symbols := make([]Symbol, 0)

	for _, targetLine := range targetLines {
		if targetLine < 1 || targetLine > len(lines) || targetLine > len(offsets) {
			continue
		}
		line := lines[targetLine-1]
		indent := 0
		for indent < len(line) && (line[indent] == ' ' || line[indent] == '\t') {
			indent++
		}
		if indent == len(line) {
			continue
		}
		start := offsets[targetLine-1] + indent
		end := offsets[targetLine-1] + len(line)
		node := root.DescendantForByteRange(uint(start), uint(end))
		for node != nil {
			if allowed[node.Kind()] && !seen[node.Id()] {
				seen[node.Id()] = true
				startLine := int(node.StartPosition().Row) + 1
				endLine := int(node.EndPosition().Row) + 1
				symbols = append(symbols, Symbol{
					Name:         symbolName(node, source),
					Kind:         node.Kind(),
					StartLine:    startLine,
					EndLine:      endLine,
					SnippetStart: startLine,
					SnippetEnd:   endLine,
				})
			}
			node = node.Parent()
		}
	}

	sort.SliceStable(symbols, func(i, j int) bool {
		if symbols[i].StartLine == symbols[j].StartLine {
			return symbols[i].EndLine < symbols[j].EndLine
		}
		return symbols[i].StartLine < symbols[j].StartLine
	})
	symbols = removeContainingSymbols(symbols)
	if len(symbols) > maxSymbols {
		symbols = symbols[:maxSymbols]
	}
	return symbols
}

func symbolName(node *treesitter.Node, source []byte) string {
	if name := node.ChildByFieldName("name"); name != nil {
		if value := strings.TrimSpace(name.Utf8Text(source)); value != "" {
			return value
		}
	}
	queue := []*treesitter.Node{node}
	for depth := 0; depth < 4 && len(queue) > 0; depth++ {
		var nextQueue []*treesitter.Node
		for _, current := range queue {
			for index := uint(0); index < current.NamedChildCount(); index++ {
				child := current.NamedChild(index)
				if child == nil {
					continue
				}
				if name := child.ChildByFieldName("name"); name != nil {
					if value := strings.TrimSpace(name.Utf8Text(source)); value != "" {
						return value
					}
				}
				nextQueue = append(nextQueue, child)
			}
		}
		queue = nextQueue
	}
	return node.Kind()
}

func removeContainingSymbols(symbols []Symbol) []Symbol {
	if len(symbols) < 2 {
		return symbols
	}
	filtered := make([]Symbol, 0, len(symbols))
	for _, candidate := range symbols {
		hasNestedSymbol := false
		for _, other := range symbols {
			if candidate == other {
				continue
			}
			if other.StartLine >= candidate.StartLine && other.EndLine <= candidate.EndLine {
				hasNestedSymbol = true
				break
			}
		}
		if !hasNestedSymbol {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func buildSymbolContent(lines []string, symbols []Symbol, targetLines []int, maxLines int) (string, bool) {
	var builder strings.Builder
	remaining := maxLines
	truncated := false

	for index, symbol := range symbols {
		if remaining <= 2 {
			truncated = true
			builder.WriteString("\n[additional context omitted by tool limit]\n")
			break
		}

		symbolStart := symbol.StartLine
		symbolEnd := symbol.EndLine
		symbol.Truncated = false
		if symbolEnd-symbolStart+1 > remaining {
			focusLine := firstTargetInRange(symbol, targetLines)
			windowStart := symbolStart
			if focusLine > 0 {
				windowStart = focusLine - remaining/2
			}
			if windowStart < symbolStart {
				windowStart = symbolStart
			}
			windowEnd := windowStart + remaining - 1
			if windowEnd > symbolEnd {
				windowEnd = symbolEnd
				windowStart = max(symbolStart, windowEnd-remaining+1)
			}
			symbolStart = windowStart
			symbolEnd = windowEnd
			symbol.Truncated = true
			truncated = true
		}
		symbols[index].SnippetStart = symbolStart
		symbols[index].SnippetEnd = symbolEnd
		symbols[index].Truncated = symbol.Truncated

		if index > 0 {
			builder.WriteString("\n")
		}
		_, _ = fmt.Fprintf(
			&builder,
			"### %s `%s` (lines %d-%d%s)\n",
			symbol.Kind,
			symbol.Name,
			symbolStart,
			symbolEnd,
			truncationSuffix(symbol.Truncated),
		)
		for lineNumber := symbolStart; lineNumber <= symbolEnd; lineNumber++ {
			_, _ = fmt.Fprintf(&builder, "%d: %s\n", lineNumber, lines[lineNumber-1])
		}
		remaining -= symbolEnd - symbolStart + 1
	}
	return strings.TrimRight(builder.String(), "\n"), truncated
}

func firstTargetInRange(symbol Symbol, targetLines []int) int {
	for _, lineNumber := range targetLines {
		if lineNumber >= symbol.StartLine && lineNumber <= symbol.EndLine {
			return lineNumber
		}
	}
	return symbol.StartLine
}

func fallbackContent(lines []string, targetLines []int, maxLines int) string {
	start := targetLines[0]
	end := targetLines[len(targetLines)-1]
	truncated := false
	if end-start+1 > maxLines {
		end = start + maxLines - 1
		if end > len(lines) {
			end = len(lines)
			start = max(1, end-maxLines+1)
		}
		truncated = true
	}
	selected := make([]string, 0, end-start+1)
	for lineNumber := start; lineNumber <= end; lineNumber++ {
		selected = append(selected, fmt.Sprintf("%d: %s", lineNumber, lines[lineNumber-1]))
	}
	content := strings.Join(selected, "\n")
	if truncated {
		content += "\n[context truncated by tool limit]"
	}
	return content
}

func lineOffsets(source []byte) []int {
	offsets := []int{0}
	for index, value := range source {
		if value == '\n' {
			offsets = append(offsets, index+1)
		}
	}
	return offsets
}

func normalizeLines(values []int) []int {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[int]bool, len(values))
	lines := make([]int, 0, len(values))
	for _, value := range values {
		if value <= 0 || seen[value] {
			continue
		}
		seen[value] = true
		lines = append(lines, value)
	}
	sort.Ints(lines)
	return lines
}

func splitLines(content string) []string {
	content = strings.TrimSuffix(content, "\r\n")
	content = strings.TrimSuffix(content, "\n")
	if content == "" {
		return nil
	}
	return strings.Split(content, "\n")
}

func truncationSuffix(truncated bool) string {
	if truncated {
		return ", truncated"
	}
	return ""
}
