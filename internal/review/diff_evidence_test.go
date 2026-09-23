package review

import (
	"encoding/json"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
	"testing"
)

func TestJSONDiffEvidenceNewSideAndBounds(t *testing.T) {
	patch := "@@ -10,3 +20,3 @@\n-old\n+new\n keep\n-last\n+added"
	for _, tc := range []struct {
		name, patch, text string
		line              int
		want              bool
	}{
		{"addition", patch, "new", 20, true},
		{"context", patch, "keep", 21, true},
		{"replacement", patch, "added", 22, true},
		{"deleted", patch, "old", 20, false},
		{"old-side line", patch, "new", 10, false},
		{"wrong line", patch, "new", 21, false},
		{"partial quote", patch, "ne", 20, false},
		{"omitted tail", "@@ -0,0 +1,2 @@\n+first", "second", 2, false},
		{"visible truncated prefix", "@@ -0,0 +1,2 @@\n+first", "first", 1, true},
		{"no header", "+new", "new", 1, false},
		{"malformed header", "@@ rubbish +1 @@\n+new", "new", 1, false},
		{"zero new count", "@@ -1 +1,0 @@\n+new", "new", 1, false},
		{"outside declared hunk", "@@ -0,0 +1 @@\n+first\n+extra", "extra", 2, false},
		{"bad context count", "@@ -0,0 +1 @@\n new", "new", 1, false},
		{"plus prefix is code", "@@ -0,0 +1 @@\n+++counter", "++counter", 1, true},
		{"second hunk", "@@ -1 +1 @@\n a\n@@ -9 +12 @@\n-old\n+new", "new", 12, true},
		{"no newline marker", "@@ -1 +1,2 @@\n-old\n+first\n\\ No newline at end of file\n+last", "last", 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := store.Evidence{Type: "reference", File: "a.go", Line: tc.line, Text: tc.text}
			file := map[string]any{"path": "a.go", "found": true, "patch": tc.patch, "truncated": true}
			for _, output := range []any{file, map[string]any{"files": []any{file}, "truncated": true}} {
				raw, _ := json.Marshal(output)
				got := parseEvidenceCorpus([]string{string(raw)}).referenceEvidence(e)
				if got != tc.want {
					t.Fatalf("matched=%v want=%v corpus=%s", got, tc.want, raw)
				}
			}
		})
	}
}

func TestJSONDiffEvidenceRejectsWrongFileAndMissingFile(t *testing.T) {
	for _, file := range []map[string]any{
		{"path": "b.go", "patch": "@@ -0,0 +1 @@\n+new"},
		{"path": "a.go", "found": false, "patch": "@@ -0,0 +1 @@\n+new"},
		{"path": "a.go", "status": "removed", "patch": "@@ -0,0 +1 @@\n+new"},
		{"path": "a.go", "patch": 42},
	} {
		raw, _ := json.Marshal(file)
		if parseEvidenceCorpus([]string{string(raw)}).referenceEvidence(store.Evidence{File: "a.go", Line: 1, Text: "new"}) {
			t.Fatalf("accepted invalid evidence: %s", raw)
		}
	}
}

func TestReviewRetainsFindingSupportedOnlyByJSONDiff(t *testing.T) {
	finding := store.Finding{File: "a.go", Line: 20, Category: "bug", Confidence: "needs_verification", Evidence: []store.Evidence{{Type: "reference", File: "a.go", Line: 20, Text: "new"}}}
	response, _ := json.Marshal(parsedReviewResponse{Summary: "Potential issue", Findings: []store.Finding{finding}})
	corpus := `{"path":"a.go","found":true,"patch":"@@ -10 +20 @@\n-old\n+new"}`
	result, err := parseReviewResponse(string(response), corpus)
	if err != nil || len(result.Findings) != 1 {
		t.Fatalf("valid diff-only finding dropped: %+v %v", result, err)
	}
	if got := DiagnoseRejectedFindings(string(response), corpus); len(got) != 0 {
		t.Fatalf("valid evidence rejected: %+v", got)
	}
	finding.Line = 19
	finding.Evidence[0].Line = 19
	response, _ = json.Marshal(parsedReviewResponse{Summary: "Potential issue", Findings: []store.Finding{finding}})
	if got := DiagnoseRejectedFindings(string(response), corpus); len(got) != 1 {
		t.Fatal("invalid line was not rejected")
	}
}
