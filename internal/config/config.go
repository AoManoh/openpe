package config

import (
	"os"
	"strings"
	"time"
)

const (
	DefaultListenAddr = "127.0.0.1:18980"
	DefaultTimeout    = 60 * time.Second
)

type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	ListenAddr string
	Timeout    time.Duration
}

func Load() Config {
	return Config{
		BaseURL:    strings.TrimSpace(os.Getenv("OPENPE_BASE_URL")),
		APIKey:     strings.TrimSpace(os.Getenv("OPENPE_API_KEY")),
		Model:      strings.TrimSpace(os.Getenv("OPENPE_MODEL")),
		ListenAddr: envOrDefault("OPENPE_LISTEN_ADDR", DefaultListenAddr),
		Timeout:    durationFromEnv("OPENPE_TIMEOUT", DefaultTimeout),
	}
}

func envOrDefault(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func durationFromEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
