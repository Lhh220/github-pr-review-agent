package main

import (
	"context"
	"log"

	"github.com/gin-gonic/gin"
	"github.com/liaohonghui/github-pr-review-agent/internal/config"
	"github.com/liaohonghui/github-pr-review-agent/internal/github"
	"github.com/liaohonghui/github-pr-review-agent/internal/llm"
	"github.com/liaohonghui/github-pr-review-agent/internal/review"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
	"github.com/liaohonghui/github-pr-review-agent/internal/taskapi"
	"github.com/liaohonghui/github-pr-review-agent/internal/webhook"
)

func main() {
	cfg := config.Load()
	if cfg.MySQLDSN == "" {
		log.Fatal("MYSQL_DSN is required")
	}
	taskStore, err := store.Open(context.Background(), cfg.MySQLDSN)
	if err != nil {
		log.Fatalf("open mysql store: %v", err)
	}
	defer taskStore.Close()

	if cfg.GitHubToken != "" {
		token := cfg.GitHubToken
		log.Printf("github auth mode: personal access token")
		gh := github.NewClient(token)
		startServer(cfg, gh, taskStore)
	} else {
		auth := appAuth(cfg)
		gh := github.NewAppClient(auth)
		log.Printf("github auth mode: GitHub App, app_id=%s, installation_id=%s", cfg.GitHubAppID, cfg.GitHubInstallationID)
		startServer(cfg, gh, taskStore)
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

func startServer(cfg *config.Config, gh *github.Client, taskStore *store.Store) {
	l := llm.New(cfg.DeepSeekAPIKey, cfg.DeepSeekBaseURL, cfg.DeepSeekModel)
	reviewer := review.New(gh, l, cfg.MaxDiffLines, cfg.MaxFileContexts, cfg.MaxFileContextLines)
	handler := webhook.New(cfg.GitHubWebhookSecret, reviewer, taskStore)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	r.POST("/webhook/github", handler.Handle)
	taskapi.New(taskStore, cfg.AdminToken).Register(r)
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})
	log.Printf("github pr review agent listening on :%s", cfg.Port)
	if err := r.Run(":" + cfg.Port); err != nil {
		log.Fatalf("server exited: %v", err)
	}
}
