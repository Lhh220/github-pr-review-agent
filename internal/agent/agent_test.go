package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/liaohonghui/github-pr-review-agent/internal/llm"
)

type echoTool struct{}

func (echoTool) Name() string { return "echo_language" }

func (echoTool) Description() string {
	return "Returns the programming language requested by the caller."
}

func (echoTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"language": map[string]any{"type": "string"},
		},
		"required": []string{"language"},
	}
}

func (echoTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	language, _ := input["language"].(string)
	if language == "" {
		return "", fmt.Errorf("language is required")
	}
	return `{"language":"` + language + `"}`, nil
}

type scriptedProvider struct {
	responses []llm.ChatResponse
	requests  []llm.ChatRequest
}

func (p *scriptedProvider) ChatWithTools(ctx context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	p.requests = append(p.requests, request)
	if len(p.responses) == 0 {
		return llm.ChatResponse{}, fmt.Errorf("no scripted response")
	}
	response := p.responses[0]
	p.responses = p.responses[1:]
	return response, nil
}

func TestRegistryBuildsDeterministicToolDefinitions(t *testing.T) {
	registry, err := NewRegistry(echoTool{})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}

	definitions := registry.Definitions()
	if len(definitions) != 1 {
		t.Fatalf("definitions length = %d, want 1", len(definitions))
	}
	if definitions[0].Name != "echo_language" || definitions[0].Parameters["type"] != "object" {
		t.Fatalf("unexpected definition: %+v", definitions[0])
	}
}

func TestRegistryRejectsDuplicateTool(t *testing.T) {
	if _, err := NewRegistry(echoTool{}, echoTool{}); err == nil {
		t.Fatal("expected duplicate tool error")
	}
}

func TestAgentRunsToolCallingLoop(t *testing.T) {
	registry, err := NewRegistry(echoTool{})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	provider := &scriptedProvider{responses: []llm.ChatResponse{
		{
			ToolCalls: []llm.ToolCall{{
				ID:        "call-1",
				Name:      "echo_language",
				Arguments: `{"language":"Go"}`,
			}},
			Usage: llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		},
		{
			Content: `{"summary":"Go review"}`,
			Usage:   llm.Usage{InputTokens: 20, OutputTokens: 7, TotalTokens: 27},
		},
	}}

	var invocations []ToolInvocation
	agent, err := New(provider, registry, Options{
		OnToolCall: func(ctx context.Context, invocation ToolInvocation) error {
			invocations = append(invocations, invocation)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	result, err := agent.Run(context.Background(), Request{
		SystemPrompt: "You review code.",
		UserPrompt:   "Review this Go pull request.",
	})
	if err != nil {
		t.Fatalf("run agent: %v", err)
	}
	if result.Content != `{"summary":"Go review"}` {
		t.Fatalf("unexpected content: %q", result.Content)
	}
	if result.ProviderCalls != 2 || len(result.ToolCalls) != 1 || len(invocations) != 1 {
		t.Fatalf("unexpected result: %+v invocations=%d", result, len(invocations))
	}
	if result.ToolCalls[0].Output != `{"language":"Go"}` || result.ToolCalls[0].Error != "" {
		t.Fatalf("unexpected tool call: %+v", result.ToolCalls[0])
	}
	if result.Usage.TotalTokens != 42 {
		t.Fatalf("total tokens = %d, want 42", result.Usage.TotalTokens)
	}
	if len(provider.requests) != 2 || len(provider.requests[0].Tools) != 1 || len(provider.requests[1].Tools) != 1 {
		t.Fatalf("unexpected provider requests: %+v", provider.requests)
	}

	messages := provider.requests[1].Messages
	if len(messages) != 5 ||
		messages[2].Role != "assistant" || messages[2].ToolCalls[0].ID != "call-1" ||
		messages[3].Role != "tool" || messages[3].ToolCallID != "call-1" ||
		messages[3].Content != `{"language":"Go"}` {
		t.Fatalf("unexpected second request messages: %+v", messages)
	}
}

func TestAgentReturnsToolErrorToProvider(t *testing.T) {
	registry, err := NewRegistry(echoTool{})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	provider := &scriptedProvider{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "missing_tool"}}},
		{Content: `{"summary":"recovered"}`},
	}}
	agent, err := New(provider, registry, Options{})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	result, err := agent.Run(context.Background(), Request{
		SystemPrompt: "You review code.",
		UserPrompt:   "Review this pull request.",
	})
	if err != nil {
		t.Fatalf("run agent: %v", err)
	}
	if result.Content != `{"summary":"recovered"}` {
		t.Fatalf("unexpected content: %q", result.Content)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Error == "" {
		t.Fatalf("expected tool error, got %+v", result.ToolCalls)
	}
	if got := provider.requests[1].Messages[3].Content; got != `Tool execution failed: agent tool "missing_tool" is not registered` {
		t.Fatalf("unexpected tool result: %q", got)
	}
}

func TestAgentGeneratesUniqueFallbackToolCallIDs(t *testing.T) {
	registry, err := NewRegistry(echoTool{})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	provider := &scriptedProvider{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{
			{Name: "echo_language", Arguments: `{"language":"Go"}`},
			{Name: "echo_language", Arguments: `{"language":"Go"}`},
		}},
		{Content: `{"summary":"completed"}`},
	}}
	agent, err := New(provider, registry, Options{})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	if _, err := agent.Run(context.Background(), Request{
		SystemPrompt: "You review code.",
		UserPrompt:   "Review this pull request.",
	}); err != nil {
		t.Fatalf("run agent: %v", err)
	}

	assistantCalls := provider.requests[1].Messages[2].ToolCalls
	if assistantCalls[0].ID == assistantCalls[1].ID {
		t.Fatalf("expected unique fallback IDs, got %+v", assistantCalls)
	}
	if provider.requests[1].Messages[3].ToolCallID != assistantCalls[0].ID ||
		provider.requests[1].Messages[4].ToolCallID != assistantCalls[1].ID {
		t.Fatalf("tool results do not match fallback IDs: %+v", provider.requests[1].Messages)
	}
}

func TestAgentForcesFinalAnswerAfterToolBudget(t *testing.T) {
	registry, err := NewRegistry(echoTool{})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	provider := &scriptedProvider{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "echo_language", Arguments: `{"language":"Go"}`}}},
		{ToolCalls: []llm.ToolCall{{ID: "call-2", Name: "echo_language", Arguments: `{"language":"Go"}`}}},
		{Content: `{"summary":"budget exhausted"}`},
	}}
	agent, err := New(provider, registry, Options{MaxSteps: 2})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	result, err := agent.Run(context.Background(), Request{
		SystemPrompt: "You review code.",
		UserPrompt:   "Review this pull request.",
	})
	if err != nil {
		t.Fatalf("run agent: %v", err)
	}
	if result.Content != `{"summary":"budget exhausted"}` || len(result.ToolCalls) != 2 || result.ProviderCalls != 3 {
		t.Fatalf("unexpected result: %+v", result)
	}
	lastRequest := provider.requests[len(provider.requests)-1]
	if len(lastRequest.Tools) != 0 || lastRequest.Messages[len(lastRequest.Messages)-1].Role != "user" {
		t.Fatalf("expected forced final request: %+v", lastRequest)
	}
}

func TestFinalResponseRepairIsBounded(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		for _, repair := range []string{`{"summary":"clean","findings":[]}`, "still invalid"} {
			t.Run(fmt.Sprintf("exhausted=%v/repair=%s", exhausted, repair), func(t *testing.T) {
				registry, err := NewRegistry(echoTool{})
				if err != nil {
					t.Fatal(err)
				}
				responses := []llm.ChatResponse{}
				if exhausted {
					responses = append(responses, llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: "call", Name: "echo_language", Arguments: `{"language":"Go"}`}}})
				}
				responses = append(responses, llm.ChatResponse{Content: "The code looks good.\n" + `{"summary":"clean","findings":[]}`, Usage: llm.Usage{TotalTokens: 10}}, llm.ChatResponse{Content: repair, Usage: llm.Usage{TotalTokens: 7}})
				provider := &scriptedProvider{responses: responses}
				runner, err := New(provider, registry, Options{MaxSteps: 1, ValidateResponse: func(s string) error {
					if !json.Valid([]byte(s)) {
						return fmt.Errorf("invalid JSON")
					}
					return nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				result, err := runner.Run(context.Background(), Request{SystemPrompt: "Return JSON", UserPrompt: "Review"})
				if (err != nil) != (repair == "still invalid") {
					t.Fatalf("unexpected error: %v", err)
				}
				wantCalls := 2
				if exhausted {
					wantCalls++
				}
				if len(provider.requests) != wantCalls || result.ProviderCalls != wantCalls || result.Usage.TotalTokens != 17 {
					t.Fatalf("unbounded retry or lost usage: %+v", result)
				}
				last := provider.requests[len(provider.requests)-1]
				if len(last.Tools) != 0 {
					t.Fatal("repair must not offer tools")
				}
				if exhausted {
					found := false
					for _, m := range last.Messages {
						if m.Role == "tool" {
							found = true
						}
					}
					if !found {
						t.Fatal("repair lost collected evidence")
					}
				}
			})
		}
	}
}

func TestReadCacheIsCanonicalAndRunScoped(t *testing.T) {
	registry, _ := NewRegistry(echoTool{})
	provider := &scriptedProvider{}
	runner, _ := New(provider, registry, Options{CacheableTools: []string{"echo_language"}})
	for run := 0; run < 2; run++ {
		provider.responses = []llm.ChatResponse{
			{ToolCalls: []llm.ToolCall{{ID: "second", Name: "echo_language", Arguments: `{ "language" : "Go" }`}}},
			{Content: "done"},
		}
		result, err := runner.Run(context.Background(), Request{SystemPrompt: "system", UserPrompt: "user",
			InitialToolCalls: []llm.ToolCall{{ID: "first", Name: "echo_language", Arguments: `{"language":"Go"}`}}})
		if err != nil {
			t.Fatal(err)
		}
		if result.ToolCalls[0].Cached || !result.ToolCalls[1].Cached || !strings.Contains(result.ToolCalls[1].Output, "first") {
			t.Fatalf("cache results: %+v", result.ToolCalls)
		}
	}
}

func TestOutputBudgetRejectsWholePayloadAndAllowsSmallerRead(t *testing.T) {
	registry, _ := NewRegistry(echoTool{})
	provider := &scriptedProvider{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "large", Name: "echo_language", Arguments: `{"language":"` + strings.Repeat("x", 100) + `"}`}}},
		{ToolCalls: []llm.ToolCall{{ID: "small", Name: "echo_language", Arguments: `{"language":"Go"}`}}},
		{Content: "done"},
	}}
	runner, _ := New(provider, registry, Options{MaxToolOutputBytes: 30})
	result, err := runner.Run(context.Background(), Request{SystemPrompt: "system", UserPrompt: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ToolCalls[0].Output != "" || result.ToolCalls[0].Error == "" || result.ToolCalls[1].Error != "" {
		t.Fatalf("unexpected results: %+v", result)
	}
}

func TestContextBudgetAlsoGuardsRepair(t *testing.T) {
	registry, _ := NewRegistry(echoTool{})
	provider := &scriptedProvider{responses: []llm.ChatResponse{{Content: strings.Repeat("x", 4096)}}}
	runner, _ := New(provider, registry, Options{MaxContextBytes: 2048, ValidateResponse: func(string) error { return fmt.Errorf("invalid JSON") }})
	_, err := runner.Run(context.Background(), Request{SystemPrompt: "system", UserPrompt: "user"})
	if err == nil || !strings.Contains(err.Error(), "context budget exceeded") || len(provider.requests) != 1 {
		t.Fatalf("err=%v requests=%d", err, len(provider.requests))
	}
	_, err = runner.Run(context.Background(), Request{SystemPrompt: "system", UserPrompt: strings.Repeat("x", 4096)})
	if err == nil || len(provider.requests) != 1 {
		t.Fatal("oversized initial request reached provider")
	}
}

type countedEchoTool struct {
	echoTool
	calls    int
	failOnce bool
}

func (t *countedEchoTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	t.calls++
	if t.failOnce {
		t.failOnce = false
		return "", fmt.Errorf("temporary failure")
	}
	return t.echoTool.Execute(ctx, input)
}
func TestCacheSkipsExecutionButRetriesFailures(t *testing.T) {
	tool := &countedEchoTool{failOnce: true}
	registry, _ := NewRegistry(tool)
	provider := &scriptedProvider{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "one", Name: "echo_language", Arguments: `{"language":"Go"}`}}},
		{ToolCalls: []llm.ToolCall{{ID: "two", Name: "echo_language", Arguments: `{"language":"Go"}`}}},
		{ToolCalls: []llm.ToolCall{{ID: "three", Name: "echo_language", Arguments: `{"language":"Go"}`}}},
		{Content: "done"},
	}}
	runner, _ := New(provider, registry, Options{CacheableTools: []string{"echo_language"}})
	result, err := runner.Run(context.Background(), Request{SystemPrompt: "system", UserPrompt: "user"})
	if err != nil || tool.calls != 2 || !result.ToolCalls[2].Cached {
		t.Fatalf("err=%v calls=%d result=%+v", err, tool.calls, result)
	}
}

func TestLowBudgetFinishesWithoutMoreReads(t *testing.T) {
	tool := &countedEchoTool{}
	registry, _ := NewRegistry(tool)
	provider := &scriptedProvider{responses: []llm.ChatResponse{
		{ToolCalls: []llm.ToolCall{{ID: "one", Name: "echo_language", Arguments: `{"language":"` + strings.Repeat("x", 80) + `"}`}, {ID: "two", Name: "echo_language", Arguments: `{"language":"Go"}`}}},
		{Content: "partial review"},
	}}
	runner, _ := New(provider, registry, Options{MaxToolOutputBytes: 100})
	result, err := runner.Run(context.Background(), Request{SystemPrompt: "system", UserPrompt: "user"})
	if err != nil || tool.calls != 1 || result.ToolCalls[1].Error == "" {
		t.Fatalf("err=%v calls=%d result=%+v", err, tool.calls, result)
	}
	last := provider.requests[len(provider.requests)-1]
	if len(last.Tools) != 0 || !strings.Contains(last.Messages[len(last.Messages)-1].Content, "uninspected") {
		t.Fatal("missing forced final/coverage notice")
	}
	if !strings.Contains(provider.requests[0].Messages[2].Content, "100 bytes") {
		t.Fatal("missing upfront budget")
	}
}

func TestRepeatedOversizeReadsStopWithinBatch(t *testing.T) {
	tool := &countedEchoTool{}
	registry, _ := NewRegistry(tool)
	call := llm.ToolCall{Name: "echo_language", Arguments: `{"language":"` + strings.Repeat("x", 200) + `"}`}
	provider := &scriptedProvider{responses: []llm.ChatResponse{{ToolCalls: []llm.ToolCall{call, call, call}}, {Content: "partial review"}}}
	runner, _ := New(provider, registry, Options{MaxToolOutputBytes: 100})
	result, err := runner.Run(context.Background(), Request{SystemPrompt: "system", UserPrompt: "user"})
	if err != nil || tool.calls != 2 || len(result.ToolCalls) != 3 || len(provider.requests[1].Tools) != 0 {
		t.Fatalf("err=%v calls=%d result=%+v", err, tool.calls, result)
	}
	for _, call := range result.ToolCalls {
		if call.Output != "" || call.Error == "" {
			t.Fatal("rejected output entered evidence")
		}
	}
}

type repairErrorProvider struct {
	firstResponse llm.ChatResponse
	firstUsage    llm.ChatResponse // usage returned alongside the deliberate error
	calls         int
}

func (p *repairErrorProvider) ChatWithTools(ctx context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	p.calls++
	if p.calls == 1 {
		return p.firstResponse, nil
	}
	// A failed repair may still carry usage in principle; it must be ignored.
	return p.firstUsage, fmt.Errorf("repair transport failure")
}

func TestFinalRepairFailureDoesNotAccumulateUsageOrProviderCalls(t *testing.T) {
	registry, err := NewRegistry(echoTool{})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	provider := &repairErrorProvider{
		firstResponse: llm.ChatResponse{Content: `{"summary":"x","findings":[]}`, Usage: llm.Usage{TotalTokens: 15}},
		firstUsage:    llm.ChatResponse{Usage: llm.Usage{TotalTokens: 999}},
	}
	runner, err := New(provider, registry, Options{
		ValidateResponse: func(string) error { return fmt.Errorf("invalid schema") },
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}

	result, runErr := runner.Run(context.Background(), Request{
		SystemPrompt: "system",
		UserPrompt:   "review",
	})
	if runErr == nil || !strings.Contains(runErr.Error(), "repair final response") {
		t.Fatalf("run error = %v, want repair failure", runErr)
	}
	if result.Usage.TotalTokens != 15 {
		t.Fatalf("usage total = %d, want 15 (failed repair usage must not accumulate)", result.Usage.TotalTokens)
	}
	if result.ProviderCalls != 1 {
		t.Fatalf("provider calls = %d, want 1 (failed repair must not count)", result.ProviderCalls)
	}
}
