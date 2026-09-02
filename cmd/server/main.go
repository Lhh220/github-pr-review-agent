package main

import (
	"log"

	"github.com/gin-gonic/gin"
	"github.com/liaohonghui/github-pr-review-agent/internal/config"
	"github.com/liaohonghui/github-pr-review-agent/internal/github"
	"github.com/liaohonghui/github-pr-review-agent/internal/llm"
	"github.com/liaohonghui/github-pr-review-agent/internal/review"
	"github.com/liaohonghui/github-pr-review-agent/internal/webhook"
)

func main() {
	cfg := config.Load()
	token, err := resolveGitHubToken(cfg)
	if err != nil {
		log.Fatalf("resolve github token: %v", err)
	}
	gh := github.NewClient(token)
	l := llm.New(cfg.DeepSeekAPIKey, cfg.DeepSeekBaseURL, cfg.DeepSeekModel)
	reviewer := review.New(gh, l, cfg.MaxDiffLines)
	handler := webhook.New(cfg.GitHubWebhookSecret, reviewer)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	r.POST("/webhook/github", handler.Handle)
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})
	log.Printf("github pr review agent listening on :%s", cfg.Port)
	if err := r.Run(":" + cfg.Port); err != nil {
		log.Fatalf("server exited: %v", err)
	}
}

func resolveGitHubToken(cfg *config.Config) (string, error) {
	if cfg.GitHubToken != "" {
		return cfg.GitHubToken, nil
	}
	auth := github.AppAuth{
		AppID:          cfg.GitHubAppID,
		InstallationID: cfg.GitHubInstallationID,
		PrivateKey:     cfg.GitHubAppPrivateKey,
		PrivateKeyPath: cfg.GitHubAppPrivateKeyPath,
	}
	return github.GetInstallationToken(auth)
}
