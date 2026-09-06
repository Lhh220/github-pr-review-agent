package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatWithToolsFormatsOpenAICompatibleRequest(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %s, want /chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"content": "",
					"tool_calls": [{
						"id": "call-1",
						"type": "function",
						"function": {
							"name": "echo_language",
							"arguments": "{\"language\":\"Go\"}"
						}
					}]
				}
			}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
		}`))
	}))
	defer server.Close()

	client := New("test-key", server.URL, "deepseek-chat")
	response, err := client.ChatWithTools(context.Background(), ChatRequest{
		Messages: []ChatMessage{
			{Role: "system", Content: "You review code."},
			{Role: "user", Content: "Review this PR."},
			{
				Role:      "assistant",
				ToolCalls: []ToolCall{{ID: "previous-call", Name: "echo_language", Arguments: "{}"}},
			},
			{Role: "tool", Content: `{"language":"Go"}`, Name: "echo_language", ToolCallID: "previous-call"},
		},
		Tools: []ToolDefinition{{
			Name:        "echo_language",
			Description: "Returns a language.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"language": map[string]any{"type": "string"},
				},
			},
		}},
	})
	if err != nil {
		t.Fatalf("chat with tools: %v", err)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].ID != "call-1" ||
		response.ToolCalls[0].Name != "echo_language" || response.ToolCalls[0].Arguments != `{"language":"Go"}` {
		t.Fatalf("unexpected response: %+v", response)
	}
	if response.Usage.TotalTokens != 15 || response.Model != "deepseek-chat" {
		t.Fatalf("unexpected usage/model: %+v", response)
	}

	tools, _ := requestBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %+v", requestBody["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Fatalf("tool type = %+v", tool["type"])
	}

	messages, _ := requestBody["messages"].([]any)
	if len(messages) != 4 {
		t.Fatalf("messages length = %d", len(messages))
	}
	assistant := messages[2].(map[string]any)
	calls, _ := assistant["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("assistant tool calls = %+v", assistant["tool_calls"])
	}
	toolMessage := messages[3].(map[string]any)
	if toolMessage["role"] != "tool" || toolMessage["tool_call_id"] != "previous-call" || toolMessage["name"] != "echo_language" {
		t.Fatalf("unexpected tool message: %+v", toolMessage)
	}
	if !strings.Contains(toolMessage["content"].(string), "Go") {
		t.Fatalf("unexpected tool content: %+v", toolMessage["content"])
	}
}
