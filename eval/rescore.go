package eval

import (
	"fmt"
)

// Rescore reuses saved outputs; it neither calls the model nor revalidates evidence.
func Rescore(source Report, cases []Case, sourcePath, datasetHash string) (Report, error) {
	expected := map[string]Expected{}
	for _, c := range cases {
		if _, ok := expected[c.Name]; ok {
			return Report{}, fmt.Errorf("duplicate case %s", c.Name)
		}
		expected[c.Name] = c.Expected
	}
	inputs := make([]CaseInput, 0, len(source.CaseResults))
	seen := map[string]bool{}
	for _, c := range source.CaseResults {
		e, ok := expected[c.Name]
		if !ok {
			return Report{}, fmt.Errorf("source case %s missing from dataset", c.Name)
		}
		key := fmt.Sprintf("%d/%s", c.Run, c.Name)
		if seen[key] {
			return Report{}, fmt.Errorf("duplicate result %s", key)
		}
		seen[key] = true
		inputs = append(inputs, CaseInput{Name: c.Name, Run: c.Run, Error: c.Error, Expected: e.Findings, Actual: RunResult{
			Findings: c.Findings, RejectedFindings: c.RejectedFindings, Responses: c.Responses, Tools: c.Tools, SkippedReason: c.SkippedReason,
			InputTokens: c.InputTokens, OutputTokens: c.OutputTokens, TotalTokens: c.TotalTokens, LatencyMS: c.LatencyMS,
		}})
	}
	if len(inputs) == 0 {
		return Report{}, fmt.Errorf("source report has no case results")
	}
	r := Evaluate(inputs)
	r.Mode = source.Mode
	r.Model = source.Model
	r.Runs = source.Runs
	r.PlannedCases = source.PlannedCases
	r.RetrievalEnabled = source.RetrievalEnabled
	r.RescoredFrom = sourcePath
	r.SourceDatasetHash = source.DatasetHash
	r.DatasetHash = datasetHash
	return r, nil
}
