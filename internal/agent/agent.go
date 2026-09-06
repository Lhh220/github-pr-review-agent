package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/liaohonghui/github-pr-review-agent/internal/llm"
)

const (
	DefaultMaxSteps    = 8
	DefaultToolTimeout = 20 * time.Second
)

type Provider interface {
	ChatWithTools(ctx context.Context, request llm.ChatRequest) (llm.ChatResponse, error)
}

type Options struct {
	MaxSteps    int
	ToolTimeout time.Duration
	OnToolCall  func(ctx context.Context, invocation ToolInvocation) error
}

type Request struct {
	SystemPrompt string
	UserPrompt   string
	MaxSteps     int
	ToolTimeout  time.Duration
}

type ToolInvocation struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Arguments  string `json:"arguments"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

type Result struct {
	Content       string           `json:"content"`
	ToolCalls     []ToolInvocation `json:"tool_calls"`
	Usage         llm.Usage        `json:"usage"`
	ProviderCalls int              `json:"provider_calls"`
}

type Agent struct {
	provider Provider
	registry *Registry
	options  Options
}

func New(provider Provider, registry *Registry, options Options) (*Agent, error) {
	if provider == nil {
		return nil, fmt.Errorf("agent provider is required")
	}
	if options.MaxSteps <= 0 {
		options.MaxSteps = DefaultMaxSteps
	}
	if options.ToolTimeout <= 0 {
		options.ToolTimeout = DefaultToolTimeout
	}
	return &Agent{
		provider: provider,
		registry: registry,
		options:  options,
	}, nil
}

func (a *Agent) Run(ctx context.Context, request Request) (Result, error) {
	if a == nil {
		return Result{}, fmt.Errorf("agent is not initialized")
	}
	if strings.TrimSpace(request.SystemPrompt) == "" {
		return Result{}, fmt.Errorf("agent system prompt is required")
	}
	if strings.TrimSpace(request.UserPrompt) == "" {
		return Result{}, fmt.Errorf("agent user prompt is required")
	}

	maxSteps := request.MaxSteps
	if maxSteps <= 0 {
		maxSteps = a.options.MaxSteps
	}
	toolTimeout := request.ToolTimeout
	if toolTimeout <= 0 {
		toolTimeout = a.options.ToolTimeout
	}

	messages := []llm.ChatMessage{
		{Role: "system", Content: request.SystemPrompt},
		{Role: "user", Content: request.UserPrompt},
	}
	tools := a.registry.Definitions()

	result := Result{ToolCalls: []ToolInvocation{}}
	for step := 0; step < maxSteps; step++ {
		response, err := a.provider.ChatWithTools(ctx, llm.ChatRequest{
			Messages: messages,
			Tools:    tools,
		})
		if err != nil {
			return result, fmt.Errorf("agent provider step %d: %w", step+1, err)
		}
		result.ProviderCalls++
		result.Usage.InputTokens += response.Usage.InputTokens
		result.Usage.OutputTokens += response.Usage.OutputTokens
		result.Usage.TotalTokens += response.Usage.TotalTokens

		if len(response.ToolCalls) == 0 {
			if response.Content == "" {
				return result, fmt.Errorf("agent provider step %d returned empty content and no tool calls", step+1)
			}
			result.Content = response.Content
			return result, nil
		}

		assistantMessage := llm.ChatMessage{
			Role:      "assistant",
			Content:   response.Content,
			ToolCalls: normalizeToolCallIDs(response.ToolCalls, step),
		}
		messages = append(messages, assistantMessage)

		for _, call := range assistantMessage.ToolCalls {
			invocation := a.executeTool(ctx, call, toolTimeout)
			result.ToolCalls = append(result.ToolCalls, invocation)
			if a.options.OnToolCall != nil {
				if err := a.options.OnToolCall(ctx, invocation); err != nil {
					return result, fmt.Errorf("record agent tool call %q: %w", call.Name, err)
				}
			}
			messages = append(messages, llm.ChatMessage{
				Role:       "tool",
				Content:    toolResultContent(invocation),
				Name:       call.Name,
				ToolCallID: call.ID,
			})
		}
	}

	messages = append(messages, llm.ChatMessage{
		Role: "user",
		Content: "The tool-calling budget is exhausted. Do not request more tools. " +
			"Return the final JSON answer now using the information already collected.",
	})
	response, err := a.provider.ChatWithTools(ctx, llm.ChatRequest{Messages: messages})
	if err != nil {
		return result, fmt.Errorf("agent final provider call: %w", err)
	}
	result.ProviderCalls++
	result.Usage.InputTokens += response.Usage.InputTokens
	result.Usage.OutputTokens += response.Usage.OutputTokens
	result.Usage.TotalTokens += response.Usage.TotalTokens
	if response.Content == "" {
		return result, fmt.Errorf("agent final provider call returned empty content")
	}
	result.Content = response.Content
	return result, nil
}

func (a *Agent) executeTool(ctx context.Context, call llm.ToolCall, timeout time.Duration) ToolInvocation {
	invocation := ToolInvocation{
		ID:        call.ID,
		Name:      call.Name,
		Arguments: call.Arguments,
	}

	toolCtx, cancel := context.WithTimeout(ctx, timeout)
	start := time.Now()
	output, err := a.registry.Execute(toolCtx, call.Name, call.Arguments)
	cancel()
	invocation.DurationMS = time.Since(start).Milliseconds()
	invocation.Output = output
	if err != nil {
		invocation.Error = err.Error()
	}
	return invocation
}

func normalizeToolCallIDs(calls []llm.ToolCall, step int) []llm.ToolCall {
	normalized := make([]llm.ToolCall, len(calls))
	copy(normalized, calls)
	for i := range normalized {
		if normalized[i].ID == "" {
			normalized[i].ID = fmt.Sprintf("tool-call-%d-%d", step+1, i+1)
		}
	}
	return normalized
}

func toolResultContent(invocation ToolInvocation) string {
	if invocation.Error != "" {
		return "Tool execution failed: " + invocation.Error
	}
	if invocation.Output == "" {
		return "Tool returned an empty result."
	}
	return invocation.Output
}
