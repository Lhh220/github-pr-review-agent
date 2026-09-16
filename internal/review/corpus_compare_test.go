package review

import (
	"reflect"
	"sort"
	"testing"

	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

// TestParsedCorpusMatchesLegacyMatching pins parsedEvidenceCorpus to the
// legacy per-call decoding (referenceEvidenceInCorpus / staticCheckEvidenceInCorpus)
// across the tricky shapes: JSON type mismatches, null, raw diff sections with
// hunk line numbers, duplicate file sections, and static-check verdicts.
func TestParsedCorpusMatchesLegacyMatching(t *testing.T) {
	refJSON := `{"path":"cmd/server/main.go","content":"42: cfg.MaxDiffLines\n43: other"}`
	refMatchesJSON := `{"matches":[{"path":"cmd/server/main.go","line":42,"snippet":"cfg.MaxDiffLines"}]}`
	checksJSON := `{"checks":[
		{"command":"go test ./...","success":false,"exit_code":1,"output":"undefined: cfg.MaxDiffLines"},
		{"command":"go vet ./...","success":true,"exit_code":0,"output":"undefined: cfg.MaxDiffLines"},
		{"command":"go test ./pkg","success":false,"exit_code":1,"timed_out":true,"output":"undefined: cfg.MaxDiffLines"},
		{"command":"go test ./x","success":false,"exit_code":1,"error":"boom","output":"undefined: cfg.MaxDiffLines"},
		{"command":"go test ./y","exit_code":1,"output":"undefined: cfg.MaxDiffLines"}
	]}`
	rawSections := "### cmd/server/main.go\n@@ -1,2 +1,3 @@\n context\n+cfg.MaxDiffLines\n### cmd/server/main.go\nnot matching"
	typeMismatch := `["an","array","not","an","object"]`
	nullSource := `null`

	corpora := map[string][]string{
		"json content":   {refJSON},
		"json matches":   {refMatchesJSON, checksJSON},
		"raw diff":       {rawSections},
		"type mismatch":  {typeMismatch},
		"null":           {nullSource},
		"mixed":          {refJSON, rawSections, typeMismatch, nullSource, checksJSON},
		"empty corpus":   {},
		"non-json text":  {"cmd/server/main.go\ncfg.MaxDiffLines"},
		"duplicate only": {rawSections},
	}

	evidences := []store.Evidence{
		{File: "cmd/server/main.go", Line: 42, Text: "cfg.MaxDiffLines"},
		{File: "cmd/server/main.go", Line: 43, Text: "cfg.MaxDiffLines"},
		{File: "cmd/server/main.go", Line: 1, Text: "cfg.MaxDiffLines"},
		{File: "other/file.go", Line: 42, Text: "cfg.MaxDiffLines"},
		{File: "cmd/server/main.go", Line: 42, Text: ""},
	}

	for corpusName, corpus := range corpora {
		parsed := parseEvidenceCorpus(corpus)
		for index, evidence := range evidences {
			legacy := referenceEvidenceInCorpus(evidence, corpus)
			current := parsed.referenceEvidence(evidence)
			if legacy != current {
				t.Errorf("reference corpus=%s evidence=%d: legacy=%v parsed=%v", corpusName, index, legacy, current)
			}
		}
	}

	staticEvidences := []store.Evidence{
		{Command: "go test ./...", Excerpt: "undefined: cfg.MaxDiffLines"},
		{Command: "go vet ./...", Excerpt: "undefined: cfg.MaxDiffLines"},
		{Command: "go test ./pkg", Excerpt: "undefined: cfg.MaxDiffLines"},
		{Command: "go test ./x", Excerpt: "undefined: cfg.MaxDiffLines"},
		{Command: "go test ./y", Excerpt: "undefined: cfg.MaxDiffLines"},
		{Command: "go test ./...", Excerpt: ""},
		{Command: "go test ./...", Excerpt: "not in output"},
	}
	for corpusName, corpus := range corpora {
		parsed := parseEvidenceCorpus(corpus)
		for index, evidence := range staticEvidences {
			legacy := staticCheckEvidenceInCorpus(evidence, corpus)
			current := parsed.staticCheckEvidence(evidence)
			if legacy != current {
				t.Errorf("static corpus=%s evidence=%d: legacy=%v parsed=%v", corpusName, index, legacy, current)
			}
		}
	}

	for corpusName, corpus := range corpora {
		legacy, current := evidenceStrings(corpus), parseEvidenceCorpus(corpus).strings
		// JSON object traversal order is unspecified; matching only uses membership.
		sort.Strings(legacy)
		sort.Strings(current)
		if !reflect.DeepEqual(legacy, current) {
			t.Errorf("strings corpus=%s: legacy and parsed diverge", corpusName)
		}
	}
}

// Expected outcomes for the load-bearing cases, independent of the comparison
// above, so a shared regression in both implementations cannot pass silently.
func TestParsedCorpusExpectedOutcomes(t *testing.T) {
	checksJSON := `{"checks":[
		{"command":"go test ./...","success":false,"exit_code":1,"output":"undefined: cfg.MaxDiffLines"},
		{"command":"go vet ./...","success":true,"exit_code":0,"output":"undefined: cfg.MaxDiffLines"},
		{"command":"go test ./pkg","success":false,"exit_code":1,"timed_out":true,"output":"boom"}
	]}`
	parsed := parseEvidenceCorpus([]string{checksJSON})

	if !parsed.staticCheckEvidence(store.Evidence{Command: "go test ./...", Excerpt: "undefined: cfg.MaxDiffLines"}) {
		t.Error("failed check with matching excerpt must validate")
	}
	if parsed.staticCheckEvidence(store.Evidence{Command: "go vet ./...", Excerpt: "undefined: cfg.MaxDiffLines"}) {
		t.Error("successful check must never validate a failure excerpt")
	}
	if parsed.staticCheckEvidence(store.Evidence{Command: "go test ./pkg", Excerpt: "boom"}) {
		t.Error("timed-out check must never validate")
	}

	// A null source decodes into the struct shapes successfully, so the raw
	// fallback must never run for it.
	nullParsed := parseEvidenceCorpus([]string{"null"})
	if got := len(nullParsed.sources); got != 1 || nullParsed.sources[0].refs == nil || nullParsed.sources[0].checks == nil {
		t.Fatalf("null must decode into both shapes: %+v", nullParsed.sources)
	}

	// The mixed corpus must equal the union of both string extractions.
	if !reflect.DeepEqual(evidenceStrings([]string{"null"}), nullParsed.strings) {
		t.Fatalf("string extraction diverges on null: %v vs %v", evidenceStrings([]string{"null"}), nullParsed.strings)
	}
}
