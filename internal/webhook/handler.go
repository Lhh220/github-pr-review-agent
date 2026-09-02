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
)

type Handler struct {
	Secret  string
	Reviewer *review.Service
}

func New(secret string, reviewer *review.Service) *Handler {
	return &Handler{Secret: secret, Reviewer: reviewer}
}

type payload struct {
	Action      string `json:"action"`
	PullRequest struct {
		Number int `json:"number"`
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
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := h.Reviewer.ReviewPR(ctx, owner, repo, p.PullRequest.Number); err != nil {
			log.Printf("review pr failed: owner=%s repo=%s number=%d error=%v", owner, repo, p.PullRequest.Number, err)
		}
	}()
	c.JSON(http.StatusAccepted, gin.H{"status": "accepted"})
}
