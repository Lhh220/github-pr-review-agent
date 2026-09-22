package eval

import (
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
	"testing"
)

func TestExplicitAlternativePreservesPrimaryMetric(t *testing.T) {
	e := ExpectedFinding{Category: "bug", File: "helper.go", Line: 33, AcceptedLocations: []FindingLocation{{File: "api.go", Line: 6}}}
	for _, tc := range []struct {
		file     string
		line     int
		category string
		want     float64
	}{
		{"api.go", 6, "bug", 1}, {"api.go", 7, "bug", 0}, {"example_test.go", 6, "bug", 0}, {"api.go", 6, "performance", 0},
	} {
		r := Evaluate([]CaseInput{{Expected: []ExpectedFinding{e}, Actual: RunResult{Findings: []store.Finding{{File: tc.file, Line: tc.line, Category: tc.category}}}}})
		assertFloat(t, r.Precision, tc.want)
		assertFloat(t, r.LocationPrecision, 0)
	}
	r := Evaluate([]CaseInput{{Expected: []ExpectedFinding{e}, Actual: RunResult{Findings: []store.Finding{{File: "api.go", Line: 6, Category: "bug"}, {File: "helper.go", Line: 33, Category: "bug"}}}}})
	assertFloat(t, r.Precision, 0.5)
	assertFloat(t, r.Recall, 1)
}

func TestRescorePreservesFailuresAndProvenance(t *testing.T) {
	source := Report{Mode: "live", Model: "model", DatasetHash: "old", Runs: 1, PlannedCases: 2, RetrievalEnabled: true, CaseResults: []CaseResult{
		{Name: "ok", Run: 1, TotalTokens: 123, LatencyMS: 45, Findings: []store.Finding{{File: "api.go", Line: 6, Category: "bug"}}},
		{Name: "failed", Run: 1, Error: "timeout"},
	}}
	cases := []Case{{Name: "ok", Expected: Expected{Findings: []ExpectedFinding{{Category: "bug", File: "helper.go", Line: 33, AcceptedLocations: []FindingLocation{{File: "api.go", Line: 6}}}}}}, {Name: "failed"}}
	r, err := Rescore(source, cases, "old.json", "new")
	if err != nil {
		t.Fatal(err)
	}
	if r.FailedCases != 1 || r.ScoredCases != 1 || r.AvgTotalTokens != 123 || r.AvgLatencyMS != 45 || r.SourceDatasetHash != "old" || r.DatasetHash != "new" || !r.RetrievalEnabled || r.RescoredFrom != "old.json" {
		t.Fatalf("lost metadata: %+v", r)
	}
	assertFloat(t, r.Precision, 1)
	assertFloat(t, r.LocationPrecision, 0)
	if _, err := Rescore(source, cases[:1], "old.json", "new"); err == nil {
		t.Fatal("accepted missing case")
	}
	if _, err := Rescore(Report{}, cases, "old.json", "new"); err == nil {
		t.Fatal("accepted empty report")
	}
}
