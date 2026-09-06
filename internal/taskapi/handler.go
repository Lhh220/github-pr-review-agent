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
	"github.com/liaohonghui/github-pr-review-agent/internal/queue"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

type Store interface {
	GetTask(ctx context.Context, id uint64) (*store.Task, error)
	ListTasks(ctx context.Context, filter store.ListFilter) ([]store.Task, error)
	GetReviewResultByTaskID(ctx context.Context, taskID uint64) (*store.ReviewResult, error)
	RequeueTask(ctx context.Context, id uint64) error
	ListAuditLogs(ctx context.Context, filter store.AuditFilter) ([]store.AuditLog, error)
	GetTaskStats(ctx context.Context, filter store.StatsFilter) (*store.TaskStats, error)
}

type Handler struct {
	store      Store
	publisher  queue.Publisher
	adminToken string
}

type TaskResponse struct {
	ID           uint64     `json:"id"`
	Repo         string     `json:"repo"`
	PRNumber     int        `json:"pr_number"`
	CommitSHA    string     `json:"commit_sha"`
	Action       string     `json:"action"`
	DeliveryID   string     `json:"delivery_id"`
	Status       string     `json:"status"`
	Error        string     `json:"error,omitempty"`
	AttemptCount int        `json:"attempt_count"`
	MaxAttempts  int        `json:"max_attempts"`
	NextRetryAt  *time.Time `json:"next_retry_at,omitempty"`
	DurationMS   int64      `json:"duration_ms"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
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

func New(store Store, publisher queue.Publisher, adminToken string) *Handler {
	return &Handler{store: store, publisher: publisher, adminToken: adminToken}
}

func (h *Handler) Register(r *gin.Engine) {
	group := r.Group("/tasks", h.authorize)
	group.GET("", h.list)
	group.GET("/:id", h.get)
	group.GET("/:id/result", h.result)

	deadLetters := r.Group("/dead-letters", h.authorize)
	deadLetters.GET("", h.deadLetters)
	deadLetters.POST("/:id/requeue", h.requeue)

	auditLogs := r.Group("/audit-logs", h.authorize)
	auditLogs.GET("", h.auditLogs)
	r.GET("/stats", h.authorize, h.stats)
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
	h.listTasks(c, "", "tasks")
}

func (h *Handler) deadLetters(c *gin.Context) {
	h.listTasks(c, "dead_letter", "dead_letters")
}

func (h *Handler) listTasks(c *gin.Context, status, responseKey string) {
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
	if status == "" {
		status = c.Query("status")
	}
	tasks, err := h.store.ListTasks(c.Request.Context(), store.ListFilter{
		Repo:     c.Query("repo"),
		Status:   status,
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
	c.JSON(http.StatusOK, gin.H{responseKey: responses})
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

func (h *Handler) requeue(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid task id"})
		return
	}
	if err := h.store.RequeueTask(c.Request.Context(), id); err != nil {
		switch {
		case errors.Is(err, store.ErrTaskNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		case errors.Is(err, store.ErrTaskTransitionFailed):
			c.JSON(http.StatusConflict, gin.H{"error": "task is not dead letter"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "requeue task"})
		}
		return
	}

	if err := h.publisher.Publish(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "publish requeued task",
			"task_id": id,
			"status":  "queued",
			"message": "task is queued and will be recovered by the stale queued scanner",
		})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"task_id": id, "status": "queued"})
}

func (h *Handler) auditLogs(c *gin.Context) {
	taskID, err := strconv.ParseUint(c.Query("task_id"), 10, 64)
	if err != nil || taskID == 0 {
		if strings.TrimSpace(c.Query("task_id")) != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid task id"})
			return
		}
		taskID = 0
	}
	limit, err := parsePositiveInt(c.Query("limit"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid limit"})
		return
	}

	logs, err := h.store.ListAuditLogs(c.Request.Context(), store.AuditFilter{
		TaskID: taskID,
		Action: strings.TrimSpace(c.Query("action")),
		Limit:  limit,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list audit logs"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"audit_logs": logs})
}

func (h *Handler) stats(c *gin.Context) {
	stats, err := h.store.GetTaskStats(c.Request.Context(), store.StatsFilter{
		Repo: strings.TrimSpace(c.Query("repo")),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "get stats"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"stats": stats})
}

func newTaskResponse(task store.Task) TaskResponse {
	var durationMS int64
	if task.UpdatedAt.After(task.CreatedAt) {
		durationMS = task.UpdatedAt.Sub(task.CreatedAt).Milliseconds()
	}
	return TaskResponse{
		ID:           task.ID,
		Repo:         task.Repo,
		PRNumber:     task.PRNumber,
		CommitSHA:    task.CommitSHA,
		Action:       task.Action,
		DeliveryID:   task.DeliveryID,
		Status:       task.Status,
		Error:        task.Error,
		AttemptCount: task.AttemptCount,
		MaxAttempts:  task.MaxAttempts,
		NextRetryAt:  task.NextRetryAt,
		DurationMS:   durationMS,
		CreatedAt:    task.CreatedAt,
		UpdatedAt:    task.UpdatedAt,
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
