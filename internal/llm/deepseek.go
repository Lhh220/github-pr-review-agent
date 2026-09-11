package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/liaohonghui/github-pr-review-agent/internal/limiter"
)

type Client struct {
	apiKey  string
	baseURL string
	model   string
	http    *http.Client
	limiter limiter.Limiter
}

// ReviewQualityRules applies to both fixed-context and tool-calling reviews.
const ReviewQualityRules = `
Before reporting a finding:
- Establish a concrete trigger, the incorrect behavior, and its user-visible impact. Quoting a real source line only establishes source authenticity, not that a defect exists.
- Verify assumptions against the relevant implementation, callers, tests, and documented contract. In tool-calling mode, inspect these with tools before claiming a helper is missing behavior. In fixed-context mode, omit claims that require unavailable context.
- Look for counterevidence: existing guards, compatible producer/consumer formats, intentional validation contracts, and tests explaining a policy. An intentional policy is only a defect if you demonstrate a concrete violated requirement or regression.
- For string matching claims, work through the exact needle and input including separators, prefixes, and newlines. A similar-looking path is not proof of a match; discard a counterexample that does not actually trigger the condition.
- For version-dependent behavior, inspect the repository's language version before asserting a defect. When a field or API is removed, follow the affected callers and resolve their types before deciding whether the change breaks them.
- Do not report requests to investigate ("may not", "verify whether", "consider checking") as bugs. needs_verification is not permission to report unsupported speculation; it is for a concrete supported risk with a clearly stated remaining uncertainty.
- Do not report theoretical complexity or suggest indexing/caching without a realistic workload and evidence of material impact. Prefer no finding over an unmeasured performance concern.
- Distinguish invalid response schemas from valid empty findings arrays. Do not recommend silently accepting invalid model output without demonstrating a contract that requires it.
- Treat timeout, OOM/process kill, missing toolchain, and dependency-download errors as incomplete validation, not proof of a PR defect. Mention these limits in the summary without inventing a root cause or claiming unobserved tests passed.
- If no actionable defect survives these checks, return a concise summary with findings: []. Always return the required JSON schema; never emit tool-call markup as the final answer.
- Emit exactly one JSON object, without Markdown fences, introductory prose, examples, or trailing text. Escape code snippets inside JSON strings.
`

type Usage struct {
	InputTokens  int `json:"prompt_tokens"`
	OutputTokens int `json:"completion_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type ReviewResponse struct {
	Content    string
	Model      string
	Usage      Usage
	DurationMS int64
}

type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ChatMessage struct {
	Role       string
	Content    string
	Name       string
	ToolCallID string
	ToolCalls  []ToolCall
}

type ChatRequest struct {
	Messages []ChatMessage
	Tools    []ToolDefinition
}

type ChatResponse struct {
	Content    string
	ToolCalls  []ToolCall
	Model      string
	Usage      Usage
	DurationMS int64
}

func New(apiKey, baseURL, model string) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
		http:    &http.Client{Timeout: 120 * time.Second},
		limiter: limiter.NoopLimiter{},
	}
}

func (c *Client) SetLimiter(l limiter.Limiter) {
	if l == nil {
		l = limiter.NoopLimiter{}
	}
	c.limiter = l
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []wireMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Tools       []toolWire    `json:"tools,omitempty"`
}

type toolWire struct {
	Type     string         `json:"type"`
	Function ToolDefinition `json:"function"`
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	Name       string         `json:"name,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
}

type wireToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content   string         `json:"content"`
			ToolCalls []wireToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
}

func (c *Client) ChatWithTools(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	messages := make([]wireMessage, 0, len(request.Messages))
	for _, message := range request.Messages {
		wire := wireMessage{
			Role:       message.Role,
			Content:    message.Content,
			Name:       message.Name,
			ToolCallID: message.ToolCallID,
		}
		for _, call := range message.ToolCalls {
			toolCall := wireToolCall{
				ID:   call.ID,
				Type: "function",
			}
			toolCall.Function.Name = call.Name
			toolCall.Function.Arguments = call.Arguments
			wire.ToolCalls = append(wire.ToolCalls, toolCall)
		}
		messages = append(messages, wire)
	}

	tools := make([]toolWire, 0, len(request.Tools))
	for _, definition := range request.Tools {
		tools = append(tools, toolWire{Type: "function", Function: definition})
	}

	response, durationMS, err := c.chat(ctx, chatRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: 0.2,
		Tools:       tools,
	})
	if err != nil {
		return ChatResponse{}, err
	}
	if len(response.Choices) == 0 {
		return ChatResponse{}, errors.New("deepseek returned no choices")
	}

	choice := response.Choices[0].Message
	toolCalls := make([]ToolCall, 0, len(choice.ToolCalls))
	for _, call := range choice.ToolCalls {
		toolCalls = append(toolCalls, ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: call.Function.Arguments,
		})
	}
	if choice.Content == "" && len(toolCalls) == 0 {
		return ChatResponse{}, errors.New("deepseek returned empty response")
	}
	return ChatResponse{
		Content:    choice.Content,
		ToolCalls:  toolCalls,
		Model:      c.model,
		Usage:      response.Usage,
		DurationMS: durationMS,
	}, nil
}

func (c *Client) ReviewCode(ctx context.Context, title, body, diff, fileContext string) (ReviewResponse, error) {
	system := `You are a senior code reviewer. Return only a valid JSON object matching this schema:
{
  "summary": "short overall review summary",
  "findings": [
    {
      "category": "bug|performance|security|style",
      "file": "path/to/file.go",
      "line": 12,
      "severity": "high|medium|low",
      "comment": "specific issue",
      "suggestion": "optional fix suggestion",
      "confidence": "confirmed|needs_verification",
      "evidence": [
        {
          "type": "reference|static_check",
          "file": "path/to/file.go",
          "line": 12,
          "text": "exact source line from the supplied diff or file context"
        },
        {
          "type": "static_check",
          "command": "go test ./...",
          "excerpt": "exact failure output supplied to you"
        }
      ]
    }
  ]
}
Rules:
- Every finding must include non-empty evidence copied exactly from the supplied diff or file context; do not paraphrase or invent evidence.
- Use confirmed only when the supplied code directly proves the issue, such as an explicit invalid reference or nil map write. Otherwise use needs_verification.
- Treat architectural concerns, performance risks, and concurrency concerns that need human confirmation as needs_verification.
- Do not report pure formatting or style preferences.
- Do not invent files, line numbers, commands, or output.
- Reference evidence must use the same file and line as the finding and quote the source exactly.
- Static-check evidence must quote the failed command and output exactly. If evidence cannot be quoted exactly, omit the finding.
- Report at most five findings, and only report issues introduced or directly triggered by this pull request.
- Prioritize bugs, security risks, and performance issues over style.
- If every changed file is documentation-only, return an empty findings array.
- If the code looks good, return an empty findings array.
Be concise and specific.` + ReviewQualityRules
	user := fmt.Sprintf(
		"Pull request title: %s\n\nPull request description:\n%s\n\nChanged files diff:\n%s\n\nChanged file context:\n%s",
		title,
		body,
		diff,
		fileContext,
	)
	reqBody := chatRequest{
		Model: c.model,
		Messages: []wireMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature: 0.2,
	}
	response, durationMS, err := c.chat(ctx, reqBody)
	if err != nil {
		return ReviewResponse{}, err
	}
	if len(response.Choices) == 0 || response.Choices[0].Message.Content == "" {
		return ReviewResponse{}, errors.New("deepseek returned empty response")
	}
	return ReviewResponse{
		Content:    response.Choices[0].Message.Content,
		Model:      c.model,
		Usage:      response.Usage,
		DurationMS: durationMS,
	}, nil
}

func (c *Client) chat(ctx context.Context, request chatRequest) (chatResponse, int64, error) {
	limiterInstance := c.limiter
	if limiterInstance == nil {
		limiterInstance = limiter.NoopLimiter{}
	}
	if err := limiterInstance.Wait(ctx, "llm:deepseek"); err != nil {
		return chatResponse{}, 0, fmt.Errorf("wait llm rate limit: %w", err)
	}

	buf, err := json.Marshal(request)
	if err != nil {
		return chatResponse{}, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return chatResponse{}, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return chatResponse{}, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return chatResponse{}, 0, fmt.Errorf("deepseek api: status=%d body=%s", resp.StatusCode, string(raw))
	}
	var out chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return chatResponse{}, 0, err
	}
	return out, time.Since(start).Milliseconds(), nil
}
