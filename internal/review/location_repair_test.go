package review

import (
	"strings"
	"testing"
)

func TestRepairProductionAnchorWithRetrievedEvidence(t *testing.T) {
	corpus := []string{`{"matches":[{"path":"session/put.go","line":4,"snippet":"s.cache[key] = value"},{"path":"session/session_test.go","line":3,"snippet":"New().Put(key, value)"}]}`}
	wrong := `{"summary":"nil map write","findings":[{"category":"bug","file":"session/put.go","line":4,"severity":"high","confidence":"confirmed","comment":"nil map write","suggestion":"initialize map","evidence":[{"type":"reference","file":"session/session_test.go","line":3,"text":"New().Put(key, value)"}]}]}`
	err := validateReviewCandidate(wrong, corpus)
	if err == nil || !strings.Contains(err.Error(), "first preserve the production defect location") {
		t.Fatalf("missing production-anchor repair guidance: %v", err)
	}
	fixed := strings.Replace(wrong, `"file":"session/session_test.go","line":3,"text":"New().Put(key, value)"`, `"file":"session/put.go","line":4,"text":"s.cache[key] = value"`, 1)
	if err := validateReviewCandidate(fixed, corpus); err != nil {
		t.Fatal(err)
	}
}
