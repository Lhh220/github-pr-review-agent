package taskapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

type fakeStore struct {
	task   *store.Task
	tasks  []store.Task
	result *store.ReviewResult
	err    error

	getID        uint64
	filter       store.ListFilter
	resultTaskID uint64
	resultErr    error
}

func (f *fakeStore) GetTask(ctx context.Context, id uint64) (*store.Task, error) {
	f.getID = id
	if f.err != nil {
		return nil, f.err
	}
	return f.task, nil
}

func (f *fakeStore) ListTasks(ctx context.Context, filter store.ListFilter) ([]store.Task, error) {
	f.filter = filter
	if f.err != nil {
		return nil, f.err
	}
	return f.tasks, nil
}

func (f *fakeStore) GetReviewResultByTaskID(ctx context.Context, taskID uint64) (*store.ReviewResult, error) {
	f.resultTaskID = taskID
	if f.resultErr != nil {
		return nil, f.resultErr
	}
	return f.result, nil
}

func newTestTask() *store.Task {
	now := time.Now()
	return &store.Task{
		ID:         1,
		Repo:       "owner/repo",
		PRNumber:   12,
		CommitSHA:  "291ac5a",
		Action:     "opened",
		DeliveryID: "delivery-1",
		Status:     "done",
		CreatedAt:  now.Add(-1500 * time.Millisecond),
		UpdatedAt:  now,
	}
}

func setupRouter(store Store, adminToken string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	New(store, adminToken).Register(router)
	return router
}

func TestListTasksWithoutConfiguredToken(t *testing.T) {
	fake := &fakeStore{tasks: []store.Task{*newTestTask()}}
	router := setupRouter(fake, "")

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestTaskResponsesIncludeDuration(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		fake := &fakeStore{tasks: []store.Task{*newTestTask()}}
		router := setupRouter(fake, "")

		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
		router.ServeHTTP(recorder, req)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
		}
		var response struct {
			Tasks []TaskResponse `json:"tasks"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(response.Tasks) != 1 || response.Tasks[0].DurationMS != 1500 {
			t.Fatalf("unexpected response: %+v", response)
		}
	})

	t.Run("get", func(t *testing.T) {
		fake := &fakeStore{task: newTestTask()}
		router := setupRouter(fake, "")

		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/tasks/1", nil)
		router.ServeHTTP(recorder, req)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
		}
		var response TaskResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if response.DurationMS != 1500 {
			t.Fatalf("duration_ms = %d, want 1500", response.DurationMS)
		}
	})
}

func TestAdminTokenAuthorization(t *testing.T) {
	fake := &fakeStore{tasks: []store.Task{*newTestTask()}}
	router := setupRouter(fake, "secret-token")

	tests := []struct {
		name           string
		authorization  string
		expectedStatus int
	}{
		{name: "missing token", authorization: "", expectedStatus: http.StatusUnauthorized},
		{name: "wrong token", authorization: "Bearer wrong", expectedStatus: http.StatusUnauthorized},
		{name: "correct token", authorization: "Bearer secret-token", expectedStatus: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
			req.Header.Set("Authorization", tt.authorization)
			router.ServeHTTP(recorder, req)
			if recorder.Code != tt.expectedStatus {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, tt.expectedStatus, recorder.Body.String())
			}
		})
	}
}

func TestListTasksValidatesQueryParameters(t *testing.T) {
	fake := &fakeStore{tasks: []store.Task{}}
	router := setupRouter(fake, "")

	tests := []struct {
		name  string
		query string
	}{
		{name: "invalid pr", query: "pr=abc"},
		{name: "zero pr", query: "pr=0"},
		{name: "invalid limit", query: "limit=-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/tasks?"+tt.query, nil)
			router.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
		})
	}
}

func TestListTasksPassesFiltersToStore(t *testing.T) {
	fake := &fakeStore{tasks: []store.Task{}}
	router := setupRouter(fake, "")

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tasks?repo=owner/repo&status=done&pr=12&limit=20", nil)
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if fake.filter != (store.ListFilter{Repo: "owner/repo", Status: "done", PRNumber: 12, Limit: 20}) {
		t.Fatalf("unexpected filter: %+v", fake.filter)
	}
}

func TestGetTask(t *testing.T) {
	t.Run("valid task", func(t *testing.T) {
		fake := &fakeStore{task: newTestTask()}
		router := setupRouter(fake, "")

		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/tasks/1", nil)
		router.ServeHTTP(recorder, req)

		if recorder.Code != http.StatusOK || fake.getID != 1 {
			t.Fatalf("status = %d id = %d body = %s", recorder.Code, fake.getID, recorder.Body.String())
		}
	})
	t.Run("invalid id", func(t *testing.T) {
		router := setupRouter(&fakeStore{}, "")

		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/tasks/not-a-number", nil)
		router.ServeHTTP(recorder, req)

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
		}
	})
	t.Run("not found", func(t *testing.T) {
		router := setupRouter(&fakeStore{err: store.ErrTaskNotFound}, "")

		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/tasks/99", nil)
		router.ServeHTTP(recorder, req)

		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
		}
	})
}

func TestGetReviewResult(t *testing.T) {
	t.Run("valid result", func(t *testing.T) {
		task := newTestTask()
		result := &store.ReviewResult{
			ID:      1,
			TaskID:  task.ID,
			Summary: "No blocking issues.",
			Findings: []store.Finding{
				{
					Category:   "bug",
					File:       "internal/foo/foo.go",
					Line:       12,
					Severity:   "high",
					Comment:    "Potential nil pointer dereference.",
					Confidence: "confirmed",
				},
			},
			RawResponse:   `{"summary":"No blocking issues.","findings":[]}`,
			Model:         "deepseek-chat",
			InputTokens:   100,
			OutputTokens:  20,
			TotalTokens:   120,
			LLMDurationMS: 1234,
			CreatedAt:     time.Now(),
		}
		fake := &fakeStore{task: task, result: result}
		router := setupRouter(fake, "")

		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/tasks/1/result", nil)
		router.ServeHTTP(recorder, req)

		if recorder.Code != http.StatusOK || fake.resultTaskID != 1 {
			t.Fatalf("status = %d taskID = %d body = %s", recorder.Code, fake.resultTaskID, recorder.Body.String())
		}
		var response struct {
			Task   TaskResponse         `json:"task"`
			Result ReviewResultResponse `json:"result"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if response.Result.Summary != "No blocking issues." || len(response.Result.Findings) != 1 ||
			response.Result.Model != "deepseek-chat" || response.Result.TotalTokens != 120 {
			t.Fatalf("unexpected result response: %+v", response.Result)
		}
	})
	t.Run("result not found", func(t *testing.T) {
		fake := &fakeStore{task: newTestTask(), resultErr: store.ErrReviewResultNotFound}
		router := setupRouter(fake, "")

		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/tasks/1/result", nil)
		router.ServeHTTP(recorder, req)

		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
		}
	})
	t.Run("task not found", func(t *testing.T) {
		fake := &fakeStore{err: store.ErrTaskNotFound}
		router := setupRouter(fake, "")

		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/tasks/99/result", nil)
		router.ServeHTTP(recorder, req)

		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
		}
	})
}
