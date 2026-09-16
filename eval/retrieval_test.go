package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liaohonghui/github-pr-review-agent/internal/githubtools"
)

func TestRetrievalFixturesValidateCrossFileEvidence(t *testing.T) {
	cases, err := LoadCases("retrieval")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			result, err := RunCase(context.Background(), c, NewScriptProvider(c), RunnerOptions{EnableRetrieval: true})
			if err != nil {
				t.Fatal(err)
			}
			for _, tool := range result.Tools {
				if tool.Error != "" {
					t.Fatalf("unexpected tool error: %s: %s", tool.Name, tool.Error)
				}
			}
			report := Evaluate([]CaseInput{{Name: c.Name, Expected: c.Expected.Findings, Actual: result}})
			if report.CaseResults[0].FalsePositives != 0 || report.CaseResults[0].FalseNegatives != 0 {
				t.Fatalf("wrong findings: %+v", report.CaseResults[0])
			}
			if len(result.Findings) != len(c.Expected.Findings) || len(result.RejectedFindings) != 0 {
				t.Fatalf("retrieval evidence lost: %+v", result)
			}
		})
	}
}

// Retrieval expectations are test-only; they are never passed to the model.
func TestRetrievalFixturesReturnRequiredSource(t *testing.T) {
	cases, err := LoadCases("retrieval")
	if err != nil {
		t.Fatal(err)
	}
	type source struct {
		File string `json:"file"`
		Line int    `json:"line"`
		Text string `json:"text"`
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			var spec struct {
				Input    map[string]any `json:"input"`
				Required []source       `json:"required"`
				FollowUp *struct {
					Input    map[string]any `json:"input"`
					Required []source       `json:"required"`
				} `json:"follow_up"`
			}
			data, err := os.ReadFile(filepath.Join("retrieval", c.Name, "retrieval.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &spec); err != nil {
				t.Fatal(err)
			}
			toolkit := githubtools.NewToolkit(&fixtureGitHubClient{fixture: c.Fixture}, "eval", "repo", 1, githubtools.Options{EnableRetrieval: true})
			defer toolkit.Close()
			execute := func(name string, input map[string]any) string {
				t.Helper()
				for _, tool := range toolkit.Tools() {
					if tool.Name() == name {
						raw, err := tool.Execute(context.Background(), input)
						if err != nil {
							t.Fatal(err)
						}
						return raw
					}
				}
				t.Fatalf("missing tool %s", name)
				return ""
			}
			raw := execute("retrieve_code_context", spec.Input)
			var out struct {
				Ref           string `json:"ref"`
				ScanTruncated bool   `json:"scan_truncated"`
				Matches       []struct {
					Path    string `json:"path"`
					Line    int    `json:"line"`
					Snippet string `json:"snippet"`
				} `json:"matches"`
			}
			if err := json.Unmarshal([]byte(raw), &out); err != nil {
				t.Fatal(err)
			}
			if out.Ref != c.Fixture.HeadSHA || out.ScanTruncated {
				t.Fatalf("wrong snapshot/coverage: %s", raw)
			}
			for _, want := range spec.Required {
				found := false
				for _, got := range out.Matches {
					if got.Path == want.File && got.Line == want.Line && got.Snippet == want.Text {
						found = true
					}
				}
				if !found {
					t.Fatalf("required source not retrieved: %+v; output=%s", want, raw)
				}
			}
			if spec.FollowUp != nil {
				// The boundary fixture must really omit the early guard from top-1.
				early := spec.FollowUp.Required[0]
				for _, got := range out.Matches {
					if got.Path == early.File && got.Line == early.Line {
						t.Fatal("boundary fixture no longer tests omitted guard")
					}
				}
				var file struct {
					Path    string `json:"path"`
					Content string `json:"content"`
				}
				if err := json.Unmarshal([]byte(execute("read_file_context", spec.FollowUp.Input)), &file); err != nil {
					t.Fatal(err)
				}
				for _, want := range spec.FollowUp.Required {
					found := false
					for _, line := range strings.Split(file.Content, "\n") {
						prefix := fmt.Sprintf("%d:", want.Line)
						if file.Path == want.File && strings.HasPrefix(strings.TrimSpace(line), prefix) && strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), prefix)) == want.Text {
							found = true
						}
					}
					if !found {
						t.Fatalf("follow-up lost source %+v: %+v", want, file)
					}
				}
			}
		})
	}
}
