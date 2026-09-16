package eval

import (
	"context"
	"testing"
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
			if len(result.Findings) != len(c.Expected.Findings) || len(result.RejectedFindings) != 0 {
				t.Fatalf("retrieval evidence lost: %+v", result)
			}
		})
	}
}
