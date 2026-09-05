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
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

type fakeTaskStore struct {
	mu sync.Mutex

	task          *store.Task
	created       bool
	err           error
	createCalls   int
	statuses      []string
	statusUpdates chan string
}

func (f *fakeTaskStore) CreateTask(ctx context.Context, input store.NewTask) (*store.Task, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, false, f.err
	}
	created := f.created && f.createCalls == 0
	f.createCalls++
	return f.task, created, nil
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

type fakePublisher struct {
	mu sync.Mutex

	err       error
	taskIDs   []uint64
	onPublish func()
}

func (f *fakePublisher) Publish(ctx context.Context, taskID uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.taskIDs = append(f.taskIDs, taskID)
	if f.onPublish != nil {
		f.onPublish()
	}
	return nil
}

func (f *fakePublisher) published() []uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uint64(nil), f.taskIDs...)
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

func newWebhookTestTask() *store.Task {
	return &store.Task{
		ID:       1,
		Repo:     "owner/repo",
		PRNumber: 12,
		Status:   "received",
	}
}

func TestHandleRejectsUnexpectedEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/webhook/github", New("secret", &fakePublisher{}, &fakeTaskStore{}).Handle)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, signedRequest(http.MethodPost, "/webhook/github", webhookPayload("opened"), "issues", "secret"))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
}

func TestHandleRejectsInvalidSignature(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/webhook/github", New("secret", &fakePublisher{}, &fakeTaskStore{}).Handle)

	recorder := httptest.NewRecorder()
	req := signedRequest(http.MethodPost, "/webhook/github", webhookPayload("opened"), "pull_request", "")
	req.Header.Set("X-Hub-Signature-256", "sha256=bad-signature")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestHandleIgnoresUnsupportedAction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	publisher := &fakePublisher{}
	router.POST("/webhook/github", New("secret", publisher, &fakeTaskStore{}).Handle)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, signedRequest(http.MethodPost, "/webhook/github", webhookPayload("closed"), "pull_request", "secret"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := publisher.published(); len(got) != 0 {
		t.Fatalf("unsupported action published tasks: %v", got)
	}
}

func TestHandleQueuesPullRequestTask(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	publisher := &fakePublisher{}
	taskStore := &fakeTaskStore{
		task:          newWebhookTestTask(),
		created:       true,
		statusUpdates: make(chan string, 1),
	}
	router.POST("/webhook/github", New("secret", publisher, taskStore).Handle)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, signedRequest(http.MethodPost, "/webhook/github", webhookPayload("opened"), "pull_request", "secret"))

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body = %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"status":"queued"`) {
		t.Fatalf("unexpected response body: %s", recorder.Body.String())
	}
	if got := publisher.published(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("published task ids = %v, want [1]", got)
	}
	select {
	case status := <-taskStore.statusUpdates:
		if status != "queued" {
			t.Fatalf("task status = %s, want queued", status)
		}
	default:
		t.Fatal("task was not marked queued")
	}
}

func TestHandleMarksQueuedBeforePublish(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	taskStore := &fakeTaskStore{
		task:          newWebhookTestTask(),
		created:       true,
		statusUpdates: make(chan string, 1),
	}
	publisher := &fakePublisher{
		onPublish: func() {
			select {
			case status := <-taskStore.statusUpdates:
				if status != "queued" {
					t.Fatalf("publish observed task status = %s, want queued", status)
				}
			default:
				t.Fatal("publish happened before task was marked queued")
			}
		},
	}
	router.POST("/webhook/github", New("secret", publisher, taskStore).Handle)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, signedRequest(http.MethodPost, "/webhook/github", webhookPayload("opened"), "pull_request", "secret"))

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d, body = %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	if got := publisher.published(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("published task ids = %v, want [1]", got)
	}
}

func TestHandleDuplicateDeliveryPublishesOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	publisher := &fakePublisher{}
	taskStore := &fakeTaskStore{
		task:    newWebhookTestTask(),
		created: true,
	}
	router.POST("/webhook/github", New("secret", publisher, taskStore).Handle)

	for i, expectedStatus := range []string{"queued", "duplicate"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, signedRequest(http.MethodPost, "/webhook/github", webhookPayload("opened"), "pull_request", "secret"))
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("request %d status = %d, want %d, body = %s", i+1, recorder.Code, http.StatusAccepted, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), expectedStatus) {
			t.Fatalf("request %d body = %s, want status %s", i+1, recorder.Body.String(), expectedStatus)
		}
	}
	if got := publisher.published(); len(got) != 1 {
		t.Fatalf("duplicate delivery published %d times, want 1", len(got))
	}
}

func TestHandleMarksTaskFailedWhenPublishFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	publisher := &fakePublisher{err: context.DeadlineExceeded}
	taskStore := &fakeTaskStore{
		task:          newWebhookTestTask(),
		created:       true,
		statusUpdates: make(chan string, 2),
	}
	router.POST("/webhook/github", New("secret", publisher, taskStore).Handle)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, signedRequest(http.MethodPost, "/webhook/github", webhookPayload("opened"), "pull_request", "secret"))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d, body = %s", recorder.Code, http.StatusInternalServerError, recorder.Body.String())
	}
	for i, expectedStatus := range []string{"queued", "failed"} {
		select {
		case status := <-taskStore.statusUpdates:
			if status != expectedStatus {
				t.Fatalf("status update %d = %s, want %s", i+1, status, expectedStatus)
			}
		default:
			t.Fatalf("missing status update %d, want %s", i+1, expectedStatus)
		}
	}
}
