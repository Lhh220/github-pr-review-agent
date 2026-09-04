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
)

type Client struct {
	apiKey  string
	baseURL string
	model   string
	http    *http.Client
}

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

func New(apiKey, baseURL, model string) *Client {
	return &Client{
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
		http:    &http.Client{Timeout: 120 * time.Second},
	}
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []message `json:"messages"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
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
      "confidence": "confirmed|needs_verification"
    }
  ]
}
Focus on real bugs, performance issues, security risks, and important readability problems. Be concise and specific. If the code looks good, return an empty findings array.`
	user := fmt.Sprintf(
		"Pull request title: %s\n\nPull request description:\n%s\n\nChanged files diff:\n%s\n\nChanged file context:\n%s",
		title,
		body,
		diff,
		fileContext,
	)
	reqBody := chatRequest{
		Model: c.model,
		Messages: []message{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature: 0.2,
	}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return ReviewResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return ReviewResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return ReviewResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return ReviewResponse{}, fmt.Errorf("deepseek api: status=%d body=%s", resp.StatusCode, string(raw))
	}
	var out chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ReviewResponse{}, err
	}
	if len(out.Choices) == 0 || out.Choices[0].Message.Content == "" {
		return ReviewResponse{}, errors.New("deepseek returned empty response")
	}
	return ReviewResponse{
		Content:    out.Choices[0].Message.Content,
		Model:      c.model,
		Usage:      out.Usage,
		DurationMS: time.Since(start).Milliseconds(),
	}, nil
}
