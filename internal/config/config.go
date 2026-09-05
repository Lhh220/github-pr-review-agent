package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	AppEnv                  string
	Port                    string
	GitHubWebhookSecret     string
	GitHubToken             string
	GitHubAppID             string
	GitHubAppPrivateKey     string
	GitHubAppPrivateKeyPath string
	GitHubInstallationID    string
	DeepSeekAPIKey          string
	DeepSeekBaseURL         string
	DeepSeekModel           string
	MaxDiffLines            int
	MaxFileContexts         int
	MaxFileContextLines     int
	MySQLDSN                string
	AdminToken              string
	RabbitMQURL             string
	ReviewQueue             string
	ReviewRetryQueue        string
	ReviewDeadLetterQueue   string
	ReviewWorkers           int
	ReviewMaxAttempts       int
	ReviewRetryBaseDelay    time.Duration
	ReviewRetryMaxDelay     time.Duration
}

func Load() *Config {
	return &Config{
		AppEnv:                  getEnv("APP_ENV", "local"),
		Port:                    getEnv("PORT", "8080"),
		GitHubWebhookSecret:     getEnv("GITHUB_WEBHOOK_SECRET", ""),
		GitHubToken:             getEnv("GITHUB_TOKEN", ""),
		GitHubAppID:             getEnv("GITHUB_APP_ID", ""),
		GitHubAppPrivateKey:     getEnv("GITHUB_APP_PRIVATE_KEY", ""),
		GitHubAppPrivateKeyPath: getEnv("GITHUB_APP_PRIVATE_KEY_PATH", ""),
		GitHubInstallationID:    getEnv("GITHUB_INSTALLATION_ID", ""),
		DeepSeekAPIKey:          getEnv("DEEPSEEK_API_KEY", ""),
		DeepSeekBaseURL:         getEnv("DEEPSEEK_BASE_URL", "https://api.deepseek.com"),
		DeepSeekModel:           getEnv("DEEPSEEK_MODEL", "deepseek-chat"),
		MaxDiffLines:            getEnvInt("MAX_DIFF_LINES", 2000),
		MaxFileContexts:         getEnvInt("MAX_FILE_CONTEXTS", 10),
		MaxFileContextLines:     getEnvInt("MAX_FILE_CONTEXT_LINES", 200),
		MySQLDSN:                getEnv("MYSQL_DSN", ""),
		AdminToken:              getEnv("ADMIN_TOKEN", ""),
		RabbitMQURL:             os.Getenv("RABBITMQ_URL"),
		ReviewQueue:             getEnv("REVIEW_QUEUE", "pr.review.queue"),
		ReviewRetryQueue:        getEnv("REVIEW_RETRY_QUEUE", "pr.review.retry.queue"),
		ReviewDeadLetterQueue:   getEnv("REVIEW_DEAD_LETTER_QUEUE", "pr.review.dead_letter.queue"),
		ReviewWorkers:           getEnvInt("REVIEW_WORKERS", 4),
		ReviewMaxAttempts:       getEnvInt("REVIEW_MAX_ATTEMPTS", 3),
		ReviewRetryBaseDelay:    getEnvDuration("REVIEW_RETRY_BASE_DELAY", 30*time.Second),
		ReviewRetryMaxDelay:     getEnvDuration("REVIEW_RETRY_MAX_DELAY", 10*time.Minute),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return fallback
}
