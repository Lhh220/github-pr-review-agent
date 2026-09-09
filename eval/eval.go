package eval

import (
	"time"

	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

type Fixture struct {
	Title        string            `json:"title"`
	Body         string            `json:"body"`
	HeadSHA      string            `json:"head_sha"`
	Files        []FileChange      `json:"files"`
	FileContents map[string]string `json:"file_contents"`
	Repository   map[string]string `json:"repository"`
}

type FileChange struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Changes   int    `json:"changes"`
	Patch     string `json:"patch"`
}

type Expected struct {
	Findings []ExpectedFinding `json:"findings"`
}

type ExpectedFinding struct {
	Category   string `json:"category"`
	File       string `json:"file"`
	Line       int    `json:"line"`
	Severity   string `json:"severity"`
	Confidence string `json:"confidence"`
}

type ScriptResponse struct {
	Content      string         `json:"content"`
	ToolCalls    []ToolCallSpec `json:"tool_calls"`
	InputTokens  int            `json:"input_tokens"`
	OutputTokens int            `json:"output_tokens"`
	TotalTokens  int            `json:"total_tokens"`
	DurationMS   int64          `json:"duration_ms"`
	Model        string         `json:"model"`
}

type ToolCallSpec struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolTrace struct {
	Name       string `json:"name"`
	DurationMS int64  `json:"duration_ms"`
}

type RunResult struct {
	Findings      []store.Finding
	InputTokens   int
	OutputTokens  int
	TotalTokens   int
	LatencyMS     int64
	Tools         []ToolTrace
	SkippedReason string
}

type CaseInput struct {
	Error    string
	Name     string
	Run      int
	Expected []ExpectedFinding
	Actual   RunResult
}

type CaseResult struct {
	Error                 string            `json:"error,omitempty"`
	SkippedReason         string            `json:"skipped_reason,omitempty"`
	Findings              []store.Finding   `json:"findings"`
	Name                  string            `json:"name"`
	Run                   int               `json:"run"`
	ExpectedFindingCount  int               `json:"expected_finding_count"`
	PredictedFindingCount int               `json:"predicted_finding_count"`
	TruePositives         int               `json:"true_positives"`
	FalsePositives        int               `json:"false_positives"`
	FalseNegatives        int               `json:"false_negatives"`
	InputTokens           int               `json:"input_tokens"`
	OutputTokens          int               `json:"output_tokens"`
	TotalTokens           int               `json:"total_tokens"`
	LatencyMS             int64             `json:"latency_ms"`
	Tools                 []ToolTrace       `json:"tools"`
	FalsePositiveFindings []store.Finding   `json:"false_positive_findings"`
	MissedFindings        []ExpectedFinding `json:"missed_findings"`
}

type Report struct {
	Mode                  string       `json:"mode"`
	Model                 string       `json:"model,omitempty"`
	PlannedCases          int          `json:"planned_cases"`
	FailedCases           int          `json:"failed_cases"`
	ScoredCases           int          `json:"scored_cases"`
	NegativeCases         int          `json:"negative_cases"`
	OverconfirmedFindings int          `json:"overconfirmed_findings"`
	Cases                 int          `json:"cases"`
	Runs                  int          `json:"runs"`
	GeneratedAt           time.Time    `json:"generated_at"`
	Precision             float64      `json:"precision"`
	Recall                float64      `json:"recall"`
	FalsePositiveRate     float64      `json:"false_positive_rate"`
	CategoryAccuracy      float64      `json:"category_accuracy"`
	ConfirmedPrecision    float64      `json:"confirmed_precision"`
	AvgInputTokens        float64      `json:"avg_input_tokens"`
	AvgOutputTokens       float64      `json:"avg_output_tokens"`
	AvgTotalTokens        float64      `json:"avg_total_tokens"`
	AvgLatencyMS          float64      `json:"avg_latency_ms"`
	CaseResults           []CaseResult `json:"case_results"`
}

func Evaluate(inputs []CaseInput) Report {
	report := Report{GeneratedAt: time.Now().UTC(), CaseResults: make([]CaseResult, 0, len(inputs))}
	var (
		truePositiveCount      int
		predictedCount         int
		expectedCount          int
		confirmedPredicted     int
		confirmedTruePositive  int
		noIssueCaseCount       int
		falsePositiveCaseCount int
		correctCategoryCount   int
		locationMatchCount     int
		inputTokens            int
		outputTokens           int
		totalTokens            int
		latencyMS              int64
	)

	for _, input := range inputs {
		result := CaseResult{
			Error:                 input.Error,
			SkippedReason:         input.Actual.SkippedReason,
			Findings:              input.Actual.Findings,
			Name:                  input.Name,
			Run:                   input.Run,
			ExpectedFindingCount:  len(input.Expected),
			PredictedFindingCount: len(input.Actual.Findings),
			InputTokens:           input.Actual.InputTokens,
			OutputTokens:          input.Actual.OutputTokens,
			TotalTokens:           input.Actual.TotalTokens,
			LatencyMS:             input.Actual.LatencyMS,
			Tools:                 input.Actual.Tools,
			FalsePositiveFindings: []store.Finding{},
			MissedFindings:        []ExpectedFinding{},
		}
		if input.Error != "" {
			report.FailedCases++
			report.CaseResults = append(report.CaseResults, result)
			continue
		}
		report.ScoredCases++
		matchedExpected := make([]bool, len(input.Expected))
		for _, predicted := range input.Actual.Findings {
			predictedCount++
			if predicted.Confidence == "confirmed" {
				confirmedPredicted++
			}

			matchIndex := -1
			for i, expected := range input.Expected {
				if matchedExpected[i] || !findingsMatch(expected, predicted) {
					continue
				}
				matchIndex = i
				if expected.Category == predicted.Category {
					break
				}
			}
			if matchIndex < 0 {
				result.FalsePositiveFindings = append(result.FalsePositiveFindings, predicted)
				continue
			}
			locationMatchCount++
			if input.Expected[matchIndex].Category != predicted.Category {
				result.FalsePositiveFindings = append(result.FalsePositiveFindings, predicted)
				continue
			}
			matchedExpected[matchIndex] = true
			result.TruePositives++
			truePositiveCount++
			correctCategoryCount++
			if predicted.Confidence == "confirmed" && input.Expected[matchIndex].Confidence == "confirmed" {
				confirmedTruePositive++
			} else if predicted.Confidence == "confirmed" && input.Expected[matchIndex].Confidence == "needs_verification" {
				report.OverconfirmedFindings++
			}
		}
		for i, expected := range input.Expected {
			if !matchedExpected[i] {
				result.MissedFindings = append(result.MissedFindings, expected)
			}
		}

		result.FalsePositives = len(result.FalsePositiveFindings)
		result.FalseNegatives = len(result.MissedFindings)
		expectedCount += len(input.Expected)
		inputTokens += input.Actual.InputTokens
		outputTokens += input.Actual.OutputTokens
		totalTokens += input.Actual.TotalTokens
		latencyMS += input.Actual.LatencyMS
		if len(input.Expected) == 0 && input.Actual.SkippedReason == "" {
			noIssueCaseCount++
			if len(input.Actual.Findings) > 0 {
				falsePositiveCaseCount++
			}
		}
		report.CaseResults = append(report.CaseResults, result)
	}

	report.Cases = len(inputs)
	report.NegativeCases = noIssueCaseCount
	report.Precision = ratio(truePositiveCount, predictedCount)
	report.Recall = ratio(truePositiveCount, expectedCount)
	report.FalsePositiveRate = ratio(falsePositiveCaseCount, noIssueCaseCount)
	report.CategoryAccuracy = ratio(correctCategoryCount, locationMatchCount)
	report.ConfirmedPrecision = ratio(confirmedTruePositive, confirmedPredicted)
	report.AvgInputTokens = floatRatio(inputTokens, report.ScoredCases)
	report.AvgOutputTokens = floatRatio(outputTokens, report.ScoredCases)
	report.AvgTotalTokens = floatRatio(totalTokens, report.ScoredCases)
	report.AvgLatencyMS = floatRatio(int(latencyMS), report.ScoredCases)
	return report
}

func findingsMatch(expected ExpectedFinding, actual store.Finding) bool {
	if expected.File != actual.File {
		return false
	}
	if expected.Line == 0 {
		return true
	}
	return abs(expected.Line-actual.Line) <= 3
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func floatRatio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
