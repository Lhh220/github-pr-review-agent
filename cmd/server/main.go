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
	"github.com/liaohonghui/github-pr-review-agent/internal/limiter"
	"github.com/liaohonghui/github-pr-review-agent/internal/llm"
	"github.com/liaohonghui/github-pr-review-agent/internal/lock"
	"github.com/liaohonghui/github-pr-review-agent/internal/queue"
	"github.com/liaohonghui/github-pr-review-agent/internal/review"
	"github.com/liaohonghui/github-pr-review-agent/internal/store"
	"github.com/liaohonghui/github-pr-review-agent/internal/taskapi"
	"github.com/liaohonghui/github-pr-review-agent/internal/webhook"
	"github.com/liaohonghui/github-pr-review-agent/internal/worker"
	"github.com/redis/go-redis/v9"
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
		if cfg.RabbitMQURL == "" {
			log.Fatal("RABBITMQ_URL is required in production")
		}
		if cfg.RedisURL == "" {
			log.Fatal("REDIS_URL is required in production")
		}
	}
	if cfg.MySQLDSN == "" {
		log.Fatal("MYSQL_DSN is required")
	}
	redisURL := cfg.RedisURL
	if redisURL == "" {
		redisURL = "redis://127.0.0.1:6379/0"
	}
	redisOptions, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Fatalf("parse redis url: %v", err)
	}
	redisClient := redis.NewClient(redisOptions)
	defer redisClient.Close()
	pingCtx, cancelPing := context.WithTimeout(ctx, 5*time.Second)
	if err := redisClient.Ping(pingCtx).Err(); err != nil {
		cancelPing()
		log.Fatalf("connect redis: %v", err)
	}
	cancelPing()
	log.Printf("redis connected: addr=%s", redisOptions.Addr)

	taskStore, err := store.Open(context.Background(), cfg.MySQLDSN)
	if err != nil {
		log.Fatalf("open mysql store: %v", err)
	}
	defer taskStore.Close()

	if cfg.GitHubToken != "" {
		token := cfg.GitHubToken
		log.Printf("github auth mode: personal access token")
		gh := github.NewClient(token)
		if err := startServer(ctx, cfg, gh, taskStore, redisClient); err != nil {
			log.Fatal(err)
		}
	} else {
		auth := appAuth(cfg)
		gh := github.NewAppClient(auth)
		log.Printf("github auth mode: GitHub App, app_id=%s, installation_id=%s", cfg.GitHubAppID, cfg.GitHubInstallationID)
		if err := startServer(ctx, cfg, gh, taskStore, redisClient); err != nil {
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

func startServer(
	ctx context.Context,
	cfg *config.Config,
	gh *github.Client,
	taskStore *store.Store,
	redisClient *redis.Client,
) error {
	rabbitURL := cfg.RabbitMQURL
	if rabbitURL == "" {
		rabbitURL = "amqp://guest:guest@127.0.0.1:5672/"
	}
	broker, err := queue.OpenRabbit(rabbitURL, queue.BrokerConfig{
		Queue:           cfg.ReviewQueue,
		RetryQueue:      cfg.ReviewRetryQueue,
		DeadLetterQueue: cfg.ReviewDeadLetterQueue,
		Prefetch:        cfg.ReviewWorkers,
	})
	if err != nil {
		return fmt.Errorf("open rabbitmq broker: %w", err)
	}
	defer broker.Close()

	l := llm.New(cfg.DeepSeekAPIKey, cfg.DeepSeekBaseURL, cfg.DeepSeekModel)
	l.SetLimiter(limiter.NewRedisLimiter(redisClient, cfg.LLMRateLimit, cfg.LLMRateWindow))
	gh.SetLimiter(limiter.NewRedisLimiter(redisClient, cfg.GitHubAPIRateLimit, cfg.GitHubAPIRateWindow))
	reviewer := review.New(gh, l, taskStore, cfg.MaxDiffLines, cfg.MaxFileContexts, cfg.MaxFileContextLines)
	handler := webhook.New(cfg.GitHubWebhookSecret, broker, taskStore)
	reviewWorker := worker.New(taskStore, reviewer, broker, cfg.ReviewWorkers, worker.Options{
		MaxAttempts:    cfg.ReviewMaxAttempts,
		RetryBaseDelay: cfg.ReviewRetryBaseDelay,
		RetryMaxDelay:  cfg.ReviewRetryMaxDelay,
		RetryJitter:    cfg.ReviewRetryJitter,
		Locker:         lock.NewRedisLocker(redisClient),
		LockTTL:        cfg.ReviewLockTTL,
		LockRetryDelay: cfg.ReviewLockRetryDelay,
	})

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	workerDone := make(chan struct{})
	workerErrors := make(chan error, 1)
	go func() {
		defer close(workerDone)
		if err := reviewWorker.Start(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			workerErrors <- err
		}
	}()
	log.Printf(
		"review worker started: queue=%s retry_queue=%s dead_letter_queue=%s workers=%d max_attempts=%d",
		cfg.ReviewQueue, cfg.ReviewRetryQueue, cfg.ReviewDeadLetterQueue, cfg.ReviewWorkers, cfg.ReviewMaxAttempts,
	)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	r.POST("/webhook/github", handler.Handle)
	taskapi.New(taskStore, broker, cfg.AdminToken).Register(r)
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

	var stopError error
	select {
	case err := <-serverErrors:
		stopError = fmt.Errorf("server exited: %w", err)
	case err := <-workerErrors:
		stopError = fmt.Errorf("review worker exited: %w", err)
	case <-ctx.Done():
		log.Printf("shutdown signal received, draining in-flight requests and reviews")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown server failed: %v", err)
	}
	cancelRun()

	select {
	case <-workerDone:
		log.Printf("review worker stopped")
	case <-time.After(30 * time.Second):
		log.Printf("wait for review worker timed out")
	}
	log.Printf("server stopped gracefully")
	return stopError
}
