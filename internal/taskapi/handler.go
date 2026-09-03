package taskapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

type Store interface {
	GetTask(ctx context.Context, id uint64) (*store.Task, error)
	ListTasks(ctx context.Context, filter store.ListFilter) ([]store.Task, error)
}

type Handler struct {
	store      Store
	adminToken string
}

func New(store Store, adminToken string) *Handler {
	return &Handler{store: store, adminToken: adminToken}
}

func (h *Handler) Register(r *gin.Engine) {
	group := r.Group("/tasks", h.authorize)
	group.GET("", h.list)
	group.GET("/:id", h.get)
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
	c.JSON(http.StatusOK, gin.H{"tasks": tasks})
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
	c.JSON(http.StatusOK, task)
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
