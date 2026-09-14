package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/liaohonghui/github-pr-review-agent/eval"
	"github.com/liaohonghui/github-pr-review-agent/internal/llm"
)

func main() {
	var (
		caseRoot           = flag.String("cases", "eval/cases", "evaluation case directory")
		reportPath         = flag.String("report", "", "report output path")
		live               = flag.Bool("live", false, "call DeepSeek instead of using fixture scripts")
		runs               = flag.Int("runs", 1, "times to run each case")
		apiKey             = flag.String("api-key", os.Getenv("DEEPSEEK_API_KEY"), "DeepSeek API key")
		baseURL            = flag.String("base-url", envOrDefault("DEEPSEEK_BASE_URL", "https://api.deepseek.com"), "DeepSeek API base URL")
		model              = flag.String("model", envOrDefault("DEEPSEEK_MODEL", "deepseek-chat"), "DeepSeek model")
		timeout            = flag.Duration("timeout", 5*time.Minute, "total evaluation timeout")
		maxSteps           = flag.Int("max-steps", 8, "maximum agent steps")
		toolTimeout        = flag.Duration("tool-timeout", 20*time.Second, "timeout for each tool call")
		enableStaticChecks = flag.Bool("enable-static-checks", false, "register run_static_checks")
		staticTimeout      = flag.Duration("static-check-timeout", 2*time.Minute, "timeout for each static check command")
		staticWorkDir      = flag.String("static-check-work-dir", "", "static check work directory")
		staticGoProxy      = flag.String("static-check-go-proxy", "off", "Go proxy for static checks")
	)
	flag.Parse()
	if *runs <= 0 {
		fatal(fmt.Errorf("-runs must be positive"))
	}

	if *reportPath == "" {
		*reportPath = "eval/report-" + time.Now().UTC().Format("20060102T150405.000000000Z") + ".json"
	}
	if _, err := os.Stat(*reportPath); err == nil {
		fatal(fmt.Errorf("report already exists: %s; use a new -report path to preserve the baseline", *reportPath))
	} else if !os.IsNotExist(err) {
		fatal(err)
	}
	cases, err := eval.LoadCases(*caseRoot)
	if err != nil {
		fatal(err)
	}

	dataset, err := json.Marshal(cases)
	if err != nil {
		fatal(err)
	}
	datasetHash := fmt.Sprintf("%x", sha256.Sum256(dataset))

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	if *live && *apiKey == "" {
		fatal(fmt.Errorf("-live requires DEEPSEEK_API_KEY or -api-key"))
	}

	liveProvider := llm.New(*apiKey, *baseURL, *model)
	inputs := make([]eval.CaseInput, 0, len(cases)**runs)
	var report eval.Report
	// Checkpoint after every case, including errors; retain completed runs on timeout.
evaluation:
	for run := 1; run <= *runs; run++ {
		for _, evaluationCase := range cases {
			var provider interface {
				ChatWithTools(context.Context, llm.ChatRequest) (llm.ChatResponse, error)
			}
			if *live {
				provider = liveProvider
			} else {
				provider = eval.NewScriptProvider(evaluationCase)
			}
			started := time.Now()
			fmt.Printf("[%d/%d] run=%d case=%s started\n", len(inputs)+1, len(cases)**runs, run, evaluationCase.Name)
			stopHeartbeat := startProgressHeartbeat(evaluationCase.Name)
			result, err := eval.RunCase(ctx, evaluationCase, provider, eval.RunnerOptions{
				OnProgress: func(message string) {
					fmt.Printf("  case=%s elapsed=%s %s\n", evaluationCase.Name, time.Since(started).Round(time.Second), message)
				},
				MaxSteps:           *maxSteps,
				ToolTimeout:        *toolTimeout,
				EnableStaticChecks: *enableStaticChecks,
				StaticCheckTimeout: *staticTimeout,
				StaticCheckWorkDir: *staticWorkDir,
				StaticCheckGoProxy: *staticGoProxy,
			})
			stopHeartbeat()
			input := eval.CaseInput{
				Name:     evaluationCase.Name,
				Run:      run,
				Expected: evaluationCase.Expected.Findings,
				Actual:   result,
			}
			if err != nil {
				input.Error = err.Error()
				fmt.Fprintf(os.Stderr, "run=%d case=%s: %v\n", run, evaluationCase.Name, err)
			}
			status := "ok"
			if err != nil {
				status = "failed"
			}
			fmt.Printf("[%d/%d] case=%s status=%s elapsed=%s calls=%d tokens=%d findings=%d rejected=%d\n", len(inputs)+1, len(cases)**runs, evaluationCase.Name, status, time.Since(started).Round(time.Millisecond), len(result.Responses), result.TotalTokens, len(result.Findings), len(result.RejectedFindings))
			inputs = append(inputs, input)
			report = eval.Evaluate(inputs)
			report.DatasetHash = datasetHash
			report.Runs = *runs
			report.PlannedCases = len(cases) * *runs
			report.Mode = "offline"
			if *live {
				report.Mode = "live"
				report.Model = *model
			}
			if err := eval.WriteReport(*reportPath, report); err != nil {
				fatal(err)
			}
			if ctx.Err() != nil {
				break evaluation
			}
		}
	}

	fmt.Printf(
		"runs=%d cases=%d precision=%.2f recall=%.2f false_positive_rate=%.2f confirmed_precision=%.2f report=%s\n",
		report.Runs,
		report.Cases,
		report.Precision,
		report.Recall,
		report.FalsePositiveRate,
		report.ConfirmedPrecision,
		*reportPath,
	)
	if report.FailedCases > 0 || report.Cases < report.PlannedCases {
		fatal(fmt.Errorf("evaluation incomplete: failed=%d attempted=%d planned=%d; saved %s", report.FailedCases, report.Cases, report.PlannedCases, *reportPath))
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func startProgressHeartbeat(name string) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	started := time.Now()
	go func() {
		defer close(done)
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				fmt.Printf("  case=%s still running elapsed=%s\n", name, time.Since(started).Round(time.Second))
			}
		}
	}()
	return func() { close(stop); <-done }
}
