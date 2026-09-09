package eval

import (
	"context"
	"math"
	"testing"

	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

func TestEvaluateSeparatesDetectionAndCategory(t *testing.T) {
	report := Evaluate([]CaseInput{
		{
			Name: "labeled",
			Expected: []ExpectedFinding{
				{Category: "bug", File: "a.go", Line: 10},
				{Category: "bug", File: "b.go", Line: 10},
			},
			Actual: RunResult{Findings: []store.Finding{
				{Category: "performance", File: "a.go", Line: 10, Confidence: "needs_verification"},
				{Category: "bug", File: "b.go", Line: 10, Confidence: "confirmed"},
				{Category: "bug", File: "c.go", Line: 10, Confidence: "confirmed"},
			}},
		},
		{Name: "clean"},
	})

	if report.CaseResults[0].TruePositives != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}
	assertFloat(t, report.Precision, 2.0/3.0)
	assertFloat(t, report.Recall, 1)
	assertFloat(t, report.CategoryAccuracy, 0.5)
	assertFloat(t, report.ConfirmedPrecision, 0.5)
	assertFloat(t, report.FalsePositiveRate, 0)
}

func TestOfflineCasesRunThroughAgentTools(t *testing.T) {
	cases, err := LoadCases("cases")
	if err != nil {
		t.Fatalf("LoadCases() error = %v", err)
	}
	if len(cases) != 5 {
		t.Fatalf("case count = %d, want 5", len(cases))
	}

	inputs := make([]CaseInput, 0, len(cases))
	for _, evaluationCase := range cases {
		result, err := RunCase(
			context.Background(),
			evaluationCase,
			NewScriptProvider(evaluationCase),
			RunnerOptions{MaxSteps: 4},
		)
		if err != nil {
			t.Fatalf("RunCase(%s) error = %v", evaluationCase.Name, err)
		}
		if len(result.Findings) != len(evaluationCase.Expected.Findings) {
			t.Fatalf("%s findings = %+v, expected %+v", evaluationCase.Name, result.Findings, evaluationCase.Expected.Findings)
		}
		for _, finding := range result.Findings {
			if len(finding.Evidence) == 0 {
				t.Fatalf("%s finding has no evidence: %+v", evaluationCase.Name, finding)
			}
		}
		if evaluationCase.Name == "005-docs-only" && (len(result.Tools) != 0 || result.SkippedReason == "") {
			t.Fatalf("docs-only result = %+v", result)
		}
		inputs = append(inputs, CaseInput{
			Name:     evaluationCase.Name,
			Expected: evaluationCase.Expected.Findings,
			Actual:   result,
		})
	}

	report := Evaluate(inputs)
	assertFloat(t, report.Precision, 1)
	assertFloat(t, report.Recall, 1)
	assertFloat(t, report.CategoryAccuracy, 1)
	assertFloat(t, report.ConfirmedPrecision, 1)
	assertFloat(t, report.FalsePositiveRate, 0)
}

func assertFloat(t *testing.T, actual, expected float64) {
	t.Helper()
	if math.Abs(actual-expected) > 0.000001 {
		t.Fatalf("value = %v, want %v", actual, expected)
	}
}
