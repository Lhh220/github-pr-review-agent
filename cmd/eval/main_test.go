package main

import (
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/liaohonghui/github-pr-review-agent/eval"
)

func TestEvalCLIHelper(t *testing.T) {
	root := os.Getenv("EVAL_TEST_CASES")
	if root == "" {
		return
	}
	os.Args = []string{os.Args[0], "-cases", root, "-report", os.Getenv("EVAL_TEST_REPORT")}
	flag.CommandLine = flag.NewFlagSet("eval", flag.ExitOnError)
	main()
	os.Exit(0)
}

func TestCLIRecordsFailureAndContinues(t *testing.T) {
	cases, err := eval.LoadCases("../../eval/cases")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for i, name := range []string{"a-failed", "b-success"} {
		c := cases[0]
		if i == 0 {
			c.Script = []eval.ScriptResponse{{Content: "not JSON"}}
		}
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Join(dir, "fixture"), 0o700); err != nil {
			t.Fatal(err)
		}
		for path, value := range map[string]any{"expected.json": c.Expected, "fixture/case.json": c.Fixture, "fixture/script.json": c.Script} {
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, path), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	reportPath := filepath.Join(t.TempDir(), "report.json")
	command := exec.Command(os.Args[0], "-test.run=^TestEvalCLIHelper$")
	command.Env = append(os.Environ(), "EVAL_TEST_CASES="+root, "EVAL_TEST_REPORT="+reportPath)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("expected failing exit status: %s", output)
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("missing checkpoint: %v; output=%s", err, output)
	}
	var report eval.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.FailedCases != 1 || report.Cases != 2 || report.PlannedCases != 2 || report.ScoredCases != 1 ||
		report.CaseResults[0].Error == "" || report.CaseResults[1].TruePositives != 1 || report.Mode != "offline" {
		t.Fatalf("unexpected report: %+v", report)
	}
}
