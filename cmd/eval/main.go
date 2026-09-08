package main

import (
	"context"
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
		reportPath         = flag.String("report", "eval/report.json", "report output path")
		live               = flag.Bool("live", false, "call DeepSeek instead of using fixture scripts")
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

	cases, err := eval.LoadCases(*caseRoot)
	if err != nil {
		fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	inputs := make([]eval.CaseInput, 0, len(cases))
	for _, evaluationCase := range cases {
		var provider interface {
			ChatWithTools(context.Context, llm.ChatRequest) (llm.ChatResponse, error)
		}
		if *live {
			if *apiKey == "" {
				fatal(fmt.Errorf("-live requires DEEPSEEK_API_KEY or -api-key"))
			}
			provider = llm.New(*apiKey, *baseURL, *model)
		} else {
			provider = eval.NewScriptProvider(evaluationCase)
		}
		result, err := eval.RunCase(ctx, evaluationCase, provider, eval.RunnerOptions{
			MaxSteps:           *maxSteps,
			ToolTimeout:        *toolTimeout,
			EnableStaticChecks: *enableStaticChecks,
			StaticCheckTimeout: *staticTimeout,
			StaticCheckWorkDir: *staticWorkDir,
			StaticCheckGoProxy: *staticGoProxy,
		})
		if err != nil {
			fatal(err)
		}
		inputs = append(inputs, eval.CaseInput{
			Name:     evaluationCase.Name,
			Expected: evaluationCase.Expected.Findings,
			Actual:   result,
		})
	}

	report := eval.Evaluate(inputs)
	if err := eval.WriteReport(*reportPath, report); err != nil {
		fatal(err)
	}
	fmt.Printf(
		"cases=%d precision=%.2f recall=%.2f false_positive_rate=%.2f confirmed_precision=%.2f report=%s\n",
		report.Cases,
		report.Precision,
		report.Recall,
		report.FalsePositiveRate,
		report.ConfirmedPrecision,
		*reportPath,
	)
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
