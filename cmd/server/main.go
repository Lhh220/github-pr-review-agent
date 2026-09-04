package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := config.Load()
	if strings.EqualFold(cfg.AppEnv, "production") {
		if cfg.GitHubWebhookSecret == "" {
			log.Fatal("GITHUB_WEBHOOK_SECRET is required in production")
		}
		if cfg.AdminToken == "" {
			log.Fatal("ADMIN_TOKEN is required in production")
		}
	}
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
		if err := startServer(ctx, cfg, gh, taskStore); err != nil {
			log.Fatal(err)
		}
	} else {
		auth := appAuth(cfg)
		gh := github.NewAppClient(auth)
		log.Printf("github auth mode: GitHub App, app_id=%s, installation_id=%s", cfg.GitHubAppID, cfg.GitHubInstallationID)
		if err := startServer(ctx, cfg, gh, taskStore); err != nil {
			log.Fatal(err)
		}
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

func startServer(ctx context.Context, cfg *config.Config, gh *github.Client, taskStore *store.Store) error {
	l := llm.New(cfg.DeepSeekAPIKey, cfg.DeepSeekBaseURL, cfg.DeepSeekModel)
	reviewer := review.New(gh, l, cfg.MaxDiffLines, cfg.MaxFileContexts, cfg.MaxFileContextLines)
	handler := webhook.New(cfg.GitHubWebhookSecret, reviewer, taskStore)

	defer func() {
		waitCtx, cancelWait := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelWait()
		if err := handler.WaitContext(waitCtx); err != nil {
			log.Printf("wait for in-flight reviews timed out: %v", err)
		}
	}()

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	r.POST("/webhook/github", handler.Handle)
	taskapi.New(taskStore, cfg.AdminToken).Register(r)
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("github pr review agent listening on :%s", cfg.Port)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	select {
	case err := <-serverErrors:
		return fmt.Errorf("server exited: %w", err)
	case <-ctx.Done():
		log.Printf("shutdown signal received, draining in-flight requests and reviews")
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShutdown()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown server: %w", err)
		}
		log.Printf("server stopped gracefully")
		return nil
	}
}
