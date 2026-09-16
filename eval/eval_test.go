package eval

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

func TestEvaluateSeparatesDetectionAndCategory(t *testing.T) {
	report := Evaluate([]CaseInput{
		{
			Name: "labeled",
			Expected: []ExpectedFinding{
				{Category: "bug", File: "a.go", Line: 10},
				{Category: "bug", File: "b.go", Line: 10, Confidence: "confirmed"},
			},
			Actual: RunResult{Findings: []store.Finding{
				{Category: "performance", File: "a.go", Line: 10, Confidence: "needs_verification"},
				{Category: "bug", File: "b.go", Line: 10, Confidence: "confirmed"},
				{Category: "bug", File: "c.go", Line: 10, Confidence: "confirmed"},
			}},
		},
		{Name: "clean"},
	})

	if report.CaseResults[0].TruePositives != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	assertFloat(t, report.Precision, 1.0/3.0)
	assertFloat(t, report.Recall, 0.5)
	assertFloat(t, report.CategoryAccuracy, 0.5)
	assertFloat(t, report.ConfirmedPrecision, 0.5)
	assertFloat(t, report.FalsePositiveRate, 0)
}

func TestOfflineCasesRunThroughAgentTools(t *testing.T) {
	cases, err := LoadCases("cases")
	if err != nil {
		t.Fatalf("LoadCases() error = %v", err)
	}
	if len(cases) != 9 {
		t.Fatalf("case count = %d, want 9", len(cases))
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
		if len(evaluationCase.Expected.Findings) == 0 && evaluationCase.Name != "005-docs-only" && (len(result.Tools) == 0 || result.SkippedReason != "") {
			t.Fatalf("clean code was not reviewed: %+v", result)
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
	if report.NegativeCases != 4 {
		t.Fatalf("negative cases = %d", report.NegativeCases)
	}
}

func TestEvaluateSeparatesFailuresSkipsAndOverconfidence(t *testing.T) {
	issue := ExpectedFinding{Category: "bug", File: "a.go", Line: 10, Confidence: "needs_verification"}
	report := Evaluate([]CaseInput{
		{Name: "failed", Error: "provider timeout", Expected: []ExpectedFinding{issue}},
		{Name: "docs", Actual: RunResult{SkippedReason: "documentation"}},
		{Name: "clean", Actual: RunResult{Findings: []store.Finding{{Category: "bug", File: "b.go", Line: 1}}}},
		{Name: "overconfident", Expected: []ExpectedFinding{issue}, Actual: RunResult{Findings: []store.Finding{{Category: "bug", File: "a.go", Line: 10, Confidence: "confirmed"}}}},
	})
	if report.FailedCases != 1 || report.ScoredCases != 3 || report.NegativeCases != 1 || report.OverconfirmedFindings != 1 {
		t.Fatalf("unexpected counters: %+v", report)
	}
	assertFloat(t, report.FalsePositiveRate, 1)
	assertFloat(t, report.Recall, 1)
	assertFloat(t, report.ConfirmedPrecision, 0)
	if report.CaseResults[0].FalseNegatives != 0 || len(report.CaseResults[3].Findings) != 1 {
		t.Fatal("failure or findings lost")
	}
}

func assertFloat(t *testing.T, actual, expected float64) {
	t.Helper()
	if math.Abs(actual-expected) > 0.000001 {
		t.Fatalf("value = %v, want %v", actual, expected)
	}
}

func TestFailedParseRetainsDiagnostics(t *testing.T) {
	cases, err := LoadCases("cases")
	if err != nil {
		t.Fatal(err)
	}
	c := cases[1]
	c.Script[len(c.Script)-1].Content = "invalid final response"
	c.Script = append(c.Script, c.Script[len(c.Script)-1])
	result, err := RunCase(context.Background(), c, NewScriptProvider(c), RunnerOptions{MaxSteps: 4})
	if err == nil {
		t.Fatal("expected parse failure")
	}
	if len(result.Responses) == 0 || result.Responses[len(result.Responses)-1].Response.Content != "invalid final response" || result.TotalTokens == 0 || len(result.Tools) < 2 || result.Tools[1].Output == "" || result.Tools[1].Input == "" {
		t.Fatalf("lost failure diagnostics: %+v", result)
	}
	report := Evaluate([]CaseInput{{Name: c.Name, Error: err.Error(), Actual: result}})
	if report.FailedCases != 1 || len(report.CaseResults[0].Responses) == 0 {
		t.Fatalf("lost report diagnostics: %+v", report)
	}
}

func TestLocationMatchingDoesNotDoubleCount(t *testing.T) {
	expected := []ExpectedFinding{{File: "a.go", Line: 10, Category: "performance"}}
	actual := []store.Finding{{File: "a.go", Line: 10, Category: "bug"}, {File: "a.go", Line: 11, Category: "bug"}}
	report := Evaluate([]CaseInput{{Expected: expected, Actual: RunResult{Findings: actual}}})
	assertFloat(t, report.Precision, 0)
	assertFloat(t, report.LocationPrecision, 0.5)
	assertFloat(t, report.LocationRecall, 1)
}

func TestRepairRunsThroughEvidenceValidation(t *testing.T) {
	cases, err := LoadCases("cases")
	if err != nil {
		t.Fatal(err)
	}
	c := cases[1]
	final := c.Script[len(c.Script)-1]
	c.Script[len(c.Script)-1].Content = "The code has an issue.\n" + final.Content
	c.Script = append(c.Script, final)
	result, err := RunCase(context.Background(), c, NewScriptProvider(c), RunnerOptions{MaxSteps: 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != len(c.Expected.Findings) || len(result.Responses) != len(c.Script) {
		t.Fatalf("repair lost findings or raw attempts: %+v", result)
	}
}

func TestCrossFileCandidateCanRepairWithoutWeakeningEvidence(t *testing.T) {
	for _, outcome := range []string{"correct", "still-misaligned", "fabricated"} {
		t.Run(outcome, func(t *testing.T) {
			cases, err := LoadCases("cases")
			if err != nil {
				t.Fatal(err)
			}
			c := cases[0]
			final := c.Script[len(c.Script)-1]
			var response struct {
				Summary  string          `json:"summary"`
				Findings []store.Finding `json:"findings"`
			}
			if err := json.Unmarshal([]byte(final.Content), &response); err != nil {
				t.Fatal(err)
			}
			response.Findings[0].File = "internal/config/config.go"
			response.Findings[0].Line = 5
			data, _ := json.Marshal(response)
			c.Script[len(c.Script)-1].Content = string(data)
			if outcome == "still-misaligned" {
				final.Content = string(data)
			}
			if outcome == "fabricated" {
				response.Findings[0].File = response.Findings[0].Evidence[0].File
				response.Findings[0].Line = response.Findings[0].Evidence[0].Line
				response.Findings[0].Evidence[0].Text = "fabricated source"
				data, _ = json.Marshal(response)
				final.Content = string(data)
			}
			c.Script = append(c.Script, final)
			result, err := RunCase(context.Background(), c, NewScriptProvider(c), RunnerOptions{MaxSteps: 4})
			if outcome == "still-misaligned" {
				if err == nil {
					t.Fatal("uncorrected location must fail")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if outcome == "correct" && (len(result.Findings) != 1 || result.Findings[0].File != "cmd/server/main.go") {
					t.Fatalf("lost verified caller: %+v", result)
				}
				if outcome == "fabricated" && (len(result.Findings) != 0 || len(result.RejectedFindings) != 1) {
					t.Fatalf("accepted fabricated correction: %+v", result)
				}
			}
			if len(result.Responses) != len(c.Script) || !result.Responses[len(result.Responses)-1].Repair {
				t.Fatalf("missing bounded repair trace: %+v", result.Responses)
			}
			if !strings.Contains(result.Tools[0].Output, "go 1.25") {
				t.Fatal("language version must be retrieved before model calls")
			}
		})
	}
}

func TestIndependentCasesUseRealAgentPipeline(t *testing.T) {
	cases, err := LoadCases("holdout")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		result, err := RunCase(context.Background(), c, NewScriptProvider(c), RunnerOptions{MaxSteps: 4})
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		report := Evaluate([]CaseInput{{Name: c.Name, Expected: c.Expected.Findings, Actual: result}})
		if report.CaseResults[0].FalsePositives != 0 || report.CaseResults[0].FalseNegatives != 0 {
			t.Fatalf("%s: %+v", c.Name, report)
		}
	}
}
