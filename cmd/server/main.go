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
	if cfg.GitHubToken != "" {
		token := cfg.GitHubToken
		log.Printf("github auth mode: personal access token")
		gh := github.NewClient(token)
		startServer(cfg, gh)
	} else {
		auth := appAuth(cfg)
		gh := github.NewAppClient(auth)
		log.Printf("github auth mode: GitHub App, app_id=%s, installation_id=%s", cfg.GitHubAppID, cfg.GitHubInstallationID)
		startServer(cfg, gh)
	}
}

func appAuth(cfg *config.Config) github.AppAuth {
	return github.AppAuth{
		AppID:          cfg.GitHubAppID,
		InstallationID: cfg.GitHubInstallationID,
		PrivateKey:     cfg.GitHubAppPrivateKey,
		PrivateKeyPath: cfg.GitHubAppPrivateKeyPath,
	}
}

func startServer(cfg *config.Config, gh *github.Client) {
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
