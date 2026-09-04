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
	"github.com/liaohonghui/github-pr-review-agent/internal/queue"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
)

type Handler struct {
	Secret    string
	Store     TaskStore
	Publisher queue.Publisher
}

func New(secret string, publisher queue.Publisher, taskStore TaskStore) *Handler {
	return &Handler{Secret: secret, Store: taskStore, Publisher: publisher}
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
	if c.GetHeader("X-GitHub-Event") != "pull_request" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid event type"})
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

	publishCtx, cancelPublish := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelPublish()
	if err := h.Publisher.Publish(publishCtx, task.ID); err != nil {
		log.Printf("publish review task failed: task_id=%d error=%v", task.ID, err)
		failedCtx, cancelFailed := context.WithTimeout(context.Background(), 5*time.Second)
		if updateErr := h.Store.UpdateTaskStatus(failedCtx, task.ID, "failed", err.Error()); updateErr != nil {
			log.Printf("mark unpublished review task failed failed: task_id=%d error=%v", task.ID, updateErr)
		}
		cancelFailed()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "publish review task"})
		return
	}

	queuedCtx, cancelQueued := context.WithTimeout(context.Background(), 5*time.Second)
	if err := h.Store.UpdateTaskStatus(queuedCtx, task.ID, "queued", ""); err != nil {
		log.Printf("mark review task queued failed: task_id=%d error=%v", task.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "mark review task queued"})
		cancelQueued()
		return
	}
	cancelQueued()
	c.JSON(http.StatusAccepted, gin.H{"status": "queued", "task_id": task.ID})
}
