package config

import (
	"os"
	"strconv"
)

type Config struct {
	Port                     string
	GitHubWebhookSecret      string
	GitHubToken              string
	GitHubAppID              string
	GitHubAppPrivateKey      string
	GitHubAppPrivateKeyPath  string
	GitHubInstallationID     string
	DeepSeekAPIKey           string
	DeepSeekBaseURL          string
	DeepSeekModel            string
	MaxDiffLines             int
}

func Load() *Config {
	return &Config{
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
