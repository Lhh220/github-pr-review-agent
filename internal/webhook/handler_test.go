package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

type fakeTaskStore struct {
	task    *store.Task
	created bool
	err     error

	statuses      []string
	statusUpdates chan string
	mu            sync.Mutex
}

func (f *fakeTaskStore) CreateTask(ctx context.Context, input store.NewTask) (*store.Task, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	return f.task, f.created, nil
}

func (f *fakeTaskStore) UpdateTaskStatus(ctx context.Context, id uint64, status, taskError string) error {
	f.mu.Lock()
	f.statuses = append(f.statuses, status)
	f.mu.Unlock()
	if f.statusUpdates != nil {
		f.statusUpdates <- status
	}
	return nil
}

type fakeReviewer struct {
	calls chan struct{}
	err   error
	panic bool
}

func (f *fakeReviewer) ReviewPR(ctx context.Context, owner, repo string, number int, taskID uint64) error {
	f.calls <- struct{}{}
	if f.panic {
		panic("review exploded")
	}
	return f.err
}

func webhookPayload(action string) []byte {
	body := map[string]any{
		"action": action,
		"pull_request": map[string]any{
			"number": 12,
			"head":   map[string]string{"sha": "291ac5a"},
		},
		"repository": map[string]string{"full_name": "owner/repo"},
	}
	data, _ := json.Marshal(body)
	return data
}

func signedRequest(method, target string, body []byte, event, secret string) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		req.Header.Set("X-Hub-Signature-256", signature)
	}
	return req
}

func TestHandleRejectsUnexpectedEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	body := webhookPayload("opened")
	router.POST("/webhook/github", New("secret", &fakeReviewer{}, &fakeTaskStore{}).Handle)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, signedRequest(http.MethodPost, "/webhook/github", body, "issues", "secret"))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
}

func TestHandleRejectsInvalidSignature(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	body := webhookPayload("opened")
	router.POST("/webhook/github", New("secret", &fakeReviewer{}, &fakeTaskStore{}).Handle)

	recorder := httptest.NewRecorder()
	req := signedRequest(http.MethodPost, "/webhook/github", body, "pull_request", "")
	req.Header.Set("X-Hub-Signature-256", "sha256=bad-signature")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestHandleIgnoresUnsupportedAction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	body := webhookPayload("closed")
	router.POST("/webhook/github", New("secret", &fakeReviewer{}, &fakeTaskStore{}).Handle)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, signedRequest(http.MethodPost, "/webhook/github", body, "pull_request", "secret"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
}

func TestHandleAcceptsPullRequestAndRunsReview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	body := webhookPayload("opened")
	reviewer := &fakeReviewer{calls: make(chan struct{}, 1)}
	taskStore := &fakeTaskStore{
		task:          &store.Task{ID: 1, Repo: "owner/repo", PRNumber: 12, Status: "received"},
		created:       true,
		statusUpdates: make(chan string, 2),
	}
	router.POST("/webhook/github", New("secret", reviewer, taskStore).Handle)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, signedRequest(http.MethodPost, "/webhook/github", body, "pull_request", "secret"))

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body = %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	select {
	case <-reviewer.calls:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for review")
	}
	for _, expected := range []string{"running", "done"} {
		select {
		case status := <-taskStore.statusUpdates:
			if status != expected {
				t.Fatalf("unexpected task status: got %s, want %s", status, expected)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for task status %s", expected)
		}
	}
}

func TestHandleMarksTaskFailedWhenReviewerPanics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	body := webhookPayload("opened")
	reviewer := &fakeReviewer{calls: make(chan struct{}, 1), panic: true}
	taskStore := &fakeTaskStore{
		task:          &store.Task{ID: 1, Repo: "owner/repo", PRNumber: 12, Status: "received"},
		created:       true,
		statusUpdates: make(chan string, 2),
	}
	router.POST("/webhook/github", New("secret", reviewer, taskStore).Handle)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, signedRequest(http.MethodPost, "/webhook/github", body, "pull_request", "secret"))

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body = %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	for _, expected := range []string{"running", "failed"} {
		select {
		case status := <-taskStore.statusUpdates:
			if status != expected {
				t.Fatalf("unexpected task status: got %s, want %s", status, expected)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for task status %s", expected)
		}
	}
}
