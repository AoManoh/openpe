package config

import (
	"bufio"
	"os"
	"strings"
	"time"
)

const (
	DefaultListenAddr = "127.0.0.1:18980"
	DefaultTimeout    = 60 * time.Second
	DefaultLanguage   = "zh"
)

type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	ListenAddr string
	Timeout    time.Duration
	Language   string
}

func Load() Config {
	envFile := strings.TrimSpace(os.Getenv("OPENPE_ENV_FILE"))
	if envFile == "" {
		envFile = ".env"
	}
	fileEnv := loadDotEnv(envFile)
	return Config{
		BaseURL:    valueFromEnv("OPENPE_BASE_URL", fileEnv),
		APIKey:     valueFromEnv("OPENPE_API_KEY", fileEnv),
		Model:      valueFromEnv("OPENPE_MODEL", fileEnv),
		ListenAddr: valueOrDefault("OPENPE_LISTEN_ADDR", fileEnv, DefaultListenAddr),
		Timeout:    durationFromValue(valueFromEnv("OPENPE_TIMEOUT", fileEnv), DefaultTimeout),
		Language:   normalizeLanguage(valueOrDefault("OPENPE_LANGUAGE", fileEnv, DefaultLanguage)),
	}
}

func valueFromEnv(key string, fileEnv map[string]string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value != "" {
		return value
	}
	return strings.TrimSpace(fileEnv[key])
}

func valueOrDefault(key string, fileEnv map[string]string, fallback string) string {
	value := valueFromEnv(key, fileEnv)
	if value == "" {
		return fallback
	}
	return value
}

func durationFromValue(value string, fallback time.Duration) time.Duration {
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func normalizeLanguage(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "en", "en-us", "english":
		return "en"
	default:
		return DefaultLanguage
	}
}

func loadDotEnv(path string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	values := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, " \t") {
			continue
		}
		values[key] = trimEnvValue(value)
	}
	return values
}

func trimEnvValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		quote := value[0]
		if (quote == '\'' || quote == '"') && value[len(value)-1] == quote {
			return value[1 : len(value)-1]
		}
	}
	return value
}
