package taskapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

type Store interface {
	GetTask(ctx context.Context, id uint64) (*store.Task, error)
	ListTasks(ctx context.Context, filter store.ListFilter) ([]store.Task, error)
	GetReviewResultByTaskID(ctx context.Context, taskID uint64) (*store.ReviewResult, error)
}

type Handler struct {
	store      Store
	adminToken string
}

type TaskResponse struct {
	ID         uint64    `json:"id"`
	Repo       string    `json:"repo"`
	PRNumber   int       `json:"pr_number"`
	CommitSHA  string    `json:"commit_sha"`
	Action     string    `json:"action"`
	DeliveryID string    `json:"delivery_id"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
	DurationMS int64     `json:"duration_ms"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type ReviewResultResponse struct {
	ID            uint64          `json:"id"`
	TaskID        uint64          `json:"task_id"`
	Summary       string          `json:"summary"`
	Findings      []store.Finding `json:"findings"`
	RawResponse   string          `json:"raw_response"`
	Model         string          `json:"model"`
	InputTokens   int             `json:"input_tokens"`
	OutputTokens  int             `json:"output_tokens"`
	TotalTokens   int             `json:"total_tokens"`
	LLMDurationMS int64           `json:"llm_duration_ms"`
	CreatedAt     time.Time       `json:"created_at"`
}

func New(store Store, adminToken string) *Handler {
	return &Handler{store: store, adminToken: adminToken}
}

func (h *Handler) Register(r *gin.Engine) {
	group := r.Group("/tasks", h.authorize)
	group.GET("", h.list)
	group.GET("/:id", h.get)
	group.GET("/:id/result", h.result)
}

func (h *Handler) authorize(c *gin.Context) {
	if h.adminToken == "" {
		return
	}
	expected := "Bearer " + h.adminToken
	if subtle.ConstantTimeCompare([]byte(c.GetHeader("Authorization")), []byte(expected)) != 1 {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid admin token"})
	}
}

func (h *Handler) list(c *gin.Context) {
	prNumber, err := parsePositiveInt(c.Query("pr"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid pr"})
		return
	}
	limit, err := parsePositiveInt(c.Query("limit"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid limit"})
		return
	}
	tasks, err := h.store.ListTasks(c.Request.Context(), store.ListFilter{
		Repo:     c.Query("repo"),
		Status:   c.Query("status"),
		PRNumber: prNumber,
		Limit:    limit,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list tasks"})
		return
	}
	responses := make([]TaskResponse, 0, len(tasks))
	for _, task := range tasks {
		responses = append(responses, newTaskResponse(task))
	}
	c.JSON(http.StatusOK, gin.H{"tasks": responses})
}

func (h *Handler) get(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid task id"})
		return
	}
	task, err := h.store.GetTask(c.Request.Context(), id)
	if errors.Is(err, store.ErrTaskNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "get task"})
		return
	}
	c.JSON(http.StatusOK, newTaskResponse(*task))
}

func (h *Handler) result(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid task id"})
		return
	}
	task, err := h.store.GetTask(c.Request.Context(), id)
	if errors.Is(err, store.ErrTaskNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "get task"})
		return
	}
	result, err := h.store.GetReviewResultByTaskID(c.Request.Context(), id)
	if errors.Is(err, store.ErrReviewResultNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "review result not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "get review result"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"task":   newTaskResponse(*task),
		"result": newReviewResultResponse(*result),
	})
}

func newTaskResponse(task store.Task) TaskResponse {
	var durationMS int64
	if task.UpdatedAt.After(task.CreatedAt) {
		durationMS = task.UpdatedAt.Sub(task.CreatedAt).Milliseconds()
	}
	return TaskResponse{
		ID:         task.ID,
		Repo:       task.Repo,
		PRNumber:   task.PRNumber,
		CommitSHA:  task.CommitSHA,
		Action:     task.Action,
		DeliveryID: task.DeliveryID,
		Status:     task.Status,
		Error:      task.Error,
		DurationMS: durationMS,
		CreatedAt:  task.CreatedAt,
		UpdatedAt:  task.UpdatedAt,
	}
}

func newReviewResultResponse(result store.ReviewResult) ReviewResultResponse {
	return ReviewResultResponse{
		ID:            result.ID,
		TaskID:        result.TaskID,
		Summary:       result.Summary,
		Findings:      result.Findings,
		RawResponse:   result.RawResponse,
		Model:         result.Model,
		InputTokens:   result.InputTokens,
		OutputTokens:  result.OutputTokens,
		TotalTokens:   result.TotalTokens,
		LLMDurationMS: result.LLMDurationMS,
		CreatedAt:     result.CreatedAt,
	}
}

func parsePositiveInt(value string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return 0, errors.New("invalid integer")
	}
	return n, nil
}
