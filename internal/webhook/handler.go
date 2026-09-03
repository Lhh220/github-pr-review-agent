package webhook

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liaohonghui/github-pr-review-agent/internal/github"
	"github.com/liaohonghui/github-pr-review-agent/internal/review"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

type Handler struct {
	Secret   string
	Reviewer *review.Service
	Store    TaskStore
}

func New(secret string, reviewer *review.Service, taskStore TaskStore) *Handler {
	return &Handler{Secret: secret, Reviewer: reviewer, Store: taskStore}
}

type TaskStore interface {
	CreateTask(ctx context.Context, input store.NewTask) (*store.Task, bool, error)
	UpdateTaskStatus(ctx context.Context, id uint64, status, taskError string) error
}

type payload struct {
	Action      string `json:"action"`
	PullRequest struct {
		Number int `json:"number"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func (h *Handler) Handle(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "read body"})
		return
	}
	signature := c.GetHeader("X-Hub-Signature-256")
	if !github.VerifyWebhookSignature(h.Secret, body, signature) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid signature"})
		return
	}
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}
	switch p.Action {
	case "opened", "synchronize", "reopened":
	default:
		c.JSON(http.StatusOK, gin.H{"status": "ignored", "action": p.Action})
		return
	}
	parts := strings.SplitN(p.Repository.FullName, "/", 2)
	if len(parts) != 2 || p.PullRequest.Number == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid repository or pr number"})
		return
	}
	owner, repo := parts[0], parts[1]
	createCtx, cancelCreate := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelCreate()
	task, created, err := h.Store.CreateTask(createCtx, store.NewTask{
		Repo:       p.Repository.FullName,
		PRNumber:   p.PullRequest.Number,
		CommitSHA:  p.PullRequest.Head.SHA,
		Action:     p.Action,
		DeliveryID: c.GetHeader("X-GitHub-Delivery"),
	})
	if err != nil {
		log.Printf("create review task failed: owner=%s repo=%s number=%d error=%v", owner, repo, p.PullRequest.Number, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create review task"})
		return
	}
	if !created {
		c.JSON(http.StatusAccepted, gin.H{"status": "duplicate", "task_id": task.ID})
		return
	}

	go h.runReview(task.ID, owner, repo, p.PullRequest.Number)
	c.JSON(http.StatusAccepted, gin.H{"status": "accepted", "task_id": task.ID})
}

func (h *Handler) runReview(taskID uint64, owner, repo string, number int) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := h.Store.UpdateTaskStatus(context.Background(), taskID, "running", ""); err != nil {
		log.Printf("mark review task running failed: task_id=%d error=%v", taskID, err)
	}
	if err := h.Reviewer.ReviewPR(ctx, owner, repo, number); err != nil {
		errMessage := err.Error()
		log.Printf("review pr failed: owner=%s repo=%s number=%d task_id=%d error=%v", owner, repo, number, taskID, err)
		if updateErr := h.Store.UpdateTaskStatus(context.Background(), taskID, "failed", errMessage); updateErr != nil {
			log.Printf("mark review task failed failed: task_id=%d error=%v", taskID, updateErr)
		}
		return
	}
	if err := h.Store.UpdateTaskStatus(context.Background(), taskID, "done", ""); err != nil {
		log.Printf("mark review task done failed: task_id=%d error=%v", taskID, err)
	}
}
