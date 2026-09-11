package codecontext

import (
	"strings"
	"testing"
)

func TestExtractGoFunctionAroundTargetLine(t *testing.T) {
	content := `package auth

type Validator struct{}

func ignored() {
	return
}

func (v Validator) Validate(token string) error {
	if token == "" {
		return errEmptyToken
	}
	return nil
}

func Other() {}
`
	result := Extract(Request{
		Path:        "internal/auth/auth.go",
		Content:     content,
		TargetLines: []int{12},
		MaxLines:    20,
	})

	if result.Strategy != "tree_sitter" || result.Language != "go" {
		t.Fatalf("unexpected strategy/language: %+v", result)
	}
	if len(result.Symbols) != 1 {
		t.Fatalf("symbols = %+v, want one Validate method", result.Symbols)
	}
	symbol := result.Symbols[0]
	if symbol.Name != "Validate" || symbol.Kind != "method_declaration" {
		t.Fatalf("unexpected symbol: %+v", symbol)
	}
	if !strings.Contains(result.Content, "func (v Validator) Validate") ||
		strings.Contains(result.Content, "func ignored") || strings.Contains(result.Content, "func Other") {
		t.Fatalf("unexpected content: %s", result.Content)
	}
}

func TestExtractPythonFunctionInsteadOfContainingClass(t *testing.T) {
	content := `class Service:
    def ignored(self):
        return None

    def refresh(self):
        if self.stale:
            self.load()
        return self.value
`
	result := Extract(Request{
		Path:        "service.py",
		Content:     content,
		TargetLines: []int{6},
		MaxLines:    20,
	})

	if result.Strategy != "tree_sitter" || result.Language != "python" {
		t.Fatalf("unexpected strategy/language: %+v", result)
	}
	if len(result.Symbols) != 1 || result.Symbols[0].Name != "refresh" {
		t.Fatalf("unexpected symbols: %+v", result.Symbols)
	}
	if !strings.Contains(result.Content, "def refresh") || strings.Contains(result.Content, "def ignored") {
		t.Fatalf("unexpected content: %s", result.Content)
	}
}

func TestExtractKeepsInnerSymbolWhenClassAndMethodBothMatch(t *testing.T) {
	content := `class Service:
    def refresh(self):
        return None
`
	result := Extract(Request{
		Path:        "service.py",
		Content:     content,
		TargetLines: []int{1, 2},
		MaxLines:    20,
	})

	if len(result.Symbols) != 1 || result.Symbols[0].Name != "refresh" {
		t.Fatalf("unexpected symbols: %+v", result.Symbols)
	}
	if !strings.Contains(result.Content, "def refresh") {
		t.Fatalf("unexpected content: %s", result.Content)
	}
}

func TestExtractJavaScriptFunction(t *testing.T) {
	content := `function ignored() {
  return 1;
}

export function validate(value) {
  if (!value) {
    throw new Error("empty");
  }
  return true;
}
`
	result := Extract(Request{
		Path:        "validate.js",
		Content:     content,
		TargetLines: []int{7},
		MaxLines:    20,
	})

	if result.Strategy != "tree_sitter" || result.Language != "javascript" {
		t.Fatalf("unexpected strategy/language: %+v", result)
	}
	if len(result.Symbols) != 1 || result.Symbols[0].Name != "validate" {
		t.Fatalf("unexpected symbols: %+v", result.Symbols)
	}
	if !strings.Contains(result.Content, "function validate") || strings.Contains(result.Content, "function ignored") {
		t.Fatalf("unexpected content: %s", result.Content)
	}
}

func TestExtractFallsBackForUnsupportedLanguage(t *testing.T) {
	result := Extract(Request{
		Path:        "notes.md",
		Content:     "one\ntwo\nthree",
		TargetLines: []int{2},
		MaxLines:    10,
	})

	if result.Strategy != "line_fallback" || result.Language != "unsupported" {
		t.Fatalf("unexpected strategy/language: %+v", result)
	}
	if !strings.Contains(result.Content, "2: two") || strings.Contains(result.Content, "1: one") {
		t.Fatalf("unexpected content: %s", result.Content)
	}
}

func TestExtractFallbackDoesNotMarkExactEndAsTruncated(t *testing.T) {
	result := Extract(Request{
		Path:        "notes.md",
		Content:     "one\ntwo\nthree",
		TargetLines: []int{1},
		MaxLines:    3,
	})

	if result.Truncated || strings.Contains(result.Content, "context truncated") {
		t.Fatalf("unexpected truncation: truncated=%v content=%s", result.Truncated, result.Content)
	}
}

func TestFallbackRangeBeyondShortFile(t *testing.T) {
	result := Extract(Request{Path: "go.mod", Content: "module sample\n\ngo 1.25\n", TargetLines: []int{1, 2, 3, 80}, MaxLines: 200})
	if !strings.Contains(result.Content, "3: go 1.25") {
		t.Fatalf("lost actual content: %+v", result)
	}
	result = Extract(Request{Path: "go.mod", Content: "module sample", TargetLines: []int{80, 81}, MaxLines: 200})
	if result.Content != "" {
		t.Fatalf("out-of-range request should be empty: %+v", result)
	}
}
