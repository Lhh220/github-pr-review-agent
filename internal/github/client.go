package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const apiBaseURL = "https://api.github.com"

type Client struct {
	token       string
	tokenSource func() (string, error)
	http        *http.Client
}

func NewClient(token string) *Client {
	return &Client{
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

func NewAppClient(auth AppAuth) *Client {
	var mu sync.Mutex
	var cachedToken *InstallationToken

	return &Client{
		tokenSource: func() (string, error) {
			mu.Lock()
			defer mu.Unlock()
			if cachedToken != nil && time.Until(cachedToken.ExpiresAt) > 2*time.Minute {
				return cachedToken.Token, nil
			}
			token, err := CreateInstallationToken(auth)
			if err != nil {
				return "", err
			}
			cachedToken = token
			log.Printf(
				"github installation token refreshed: expires_at=%s repository_selection=%s repositories=[%s] permissions=[%s]",
				token.ExpiresAt.Format(time.RFC3339),
				token.RepositorySelection,
				token.RepositorySummary(),
				token.PermissionSummary(),
			)
			return token.Token, nil
		},
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

type PullRequest struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Head   Ref    `json:"head"`
	Base   Ref    `json:"base"`
}

type Ref struct {
	SHA string `json:"sha"`
	Ref string `json:"ref"`
}

type PullRequestFile struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Changes   int    `json:"changes"`
	Patch     string `json:"patch"`
}

type FileContent struct {
	Path    string
	Content string
}

type fileContentResponse struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	token := c.token
	if c.tokenSource != nil {
		var err error
		token, err = c.tokenSource()
		if err != nil {
			return fmt.Errorf("get github token: %w", err)
		}
	}
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github api %s %s: status=%d body=%s", method, path, resp.StatusCode, string(raw))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) GetPullRequest(ctx context.Context, owner, repo string, number int) (*PullRequest, error) {
	var pr PullRequest
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number)
	if err := c.do(ctx, http.MethodGet, path, nil, &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

func (c *Client) GetPullRequestFiles(ctx context.Context, owner, repo string, number int) ([]PullRequestFile, error) {
	var files []PullRequestFile
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/files?per_page=100", owner, repo, number)
	if err := c.do(ctx, http.MethodGet, path, nil, &files); err != nil {
		return nil, err
	}
	return files, nil
}

func (c *Client) GetFileContent(ctx context.Context, owner, repo, path, ref string) (string, error) {
	query := ""
	if ref != "" {
		query = "?ref=" + url.QueryEscape(ref)
	}
	apiPath := fmt.Sprintf("/repos/%s/%s/contents/%s%s", owner, repo, url.PathEscape(path), query)
	var out fileContentResponse
	if err := c.do(ctx, http.MethodGet, apiPath, nil, &out); err != nil {
		return "", err
	}
	if out.Content == "" {
		return "", nil
	}
	if out.Encoding != "base64" {
		return "", fmt.Errorf("unsupported file encoding: %s", out.Encoding)
	}
	content, err := base64.StdEncoding.DecodeString(out.Content)
	if err != nil {
		return "", fmt.Errorf("decode file content: %w", err)
	}
	return string(content), nil
}

func (c *Client) CreateIssueComment(ctx context.Context, owner, repo string, number int, body string) error {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, number)
	payload := map[string]string{"body": body}
	return c.do(ctx, http.MethodPost, path, payload, nil)
}

func (c *Client) CreatePullRequestReview(ctx context.Context, owner, repo string, number int, body string) error {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", owner, repo, number)
	payload := map[string]string{
		"body":  body,
		"event": "COMMENT",
	}
	return c.do(ctx, http.MethodPost, path, payload, nil)
}
