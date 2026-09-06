package agent

import (
	"context"
	"fmt"
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
	if len(messages) != 4 ||
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
