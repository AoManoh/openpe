package config

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultListenAddr = "127.0.0.1:18980"
	DefaultTimeout    = 60 * time.Second
	DefaultLanguage   = "zh"

	DefaultOpenaceAddr            = "127.0.0.1:8765"
	DefaultOpenaceMaxOutputLength = 12000
	DefaultOpenaceTimeout         = 30 * time.Second
	DefaultOpenaceMaxRetries      = 2
	DefaultOpenaceRetryBaseDelay  = 250 * time.Millisecond
	DefaultOpenaceRetryMaxDelay   = 2 * time.Second
	DefaultOpenaceRetryJitter     = 100 * time.Millisecond

	DefaultCodexHistoryMaxMessages = 12
	DefaultCodexHistoryMaxChars    = 12000
)

type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	ListenAddr string
	Timeout    time.Duration
	Language   string
	Openace    OpenaceConfig
	Codex      CodexConfig
	Server     ServerConfig
}

type CodexConfig struct {
	History CodexHistoryConfig
}

type CodexHistoryConfig struct {
	Enabled     bool
	Home        string
	MaxMessages int
	MaxChars    int
}

// ServerConfig collects HTTP server runtime options that are independent of
// the prompt enhancement core. Empty fields preserve the historical default
// behaviour (no authentication, no CORS, no lifecycle hooks).
type ServerConfig struct {
	// Token, when non-empty, enables bearer-token authentication on the
	// HTTP server. Use a 256-bit hex string (e.g. produced by
	// integration.GenerateToken). When LifecycleEnabled is true and Token
	// is empty, openpe-server generates an ephemeral token at startup.
	Token string
	// CORSOrigins is the list of Origin headers the server reflects in
	// Access-Control-Allow-Origin. Comma-separated in env / .env. Special
	// values: "*" allows any origin, "null" allows Electron file:// webviews.
	CORSOrigins []string
	// LifecycleEnabled controls whether openpe-server writes a descriptor
	// file at startup so IDE installers can discover its base URL and token.
	// Default false — opt in only when integrating with a patch installer
	// (Windsurf, Cursor, ...). Enabling auto-generates an ephemeral token
	// when Token is empty.
	LifecycleEnabled bool
	// DescriptorFile overrides integration.DefaultDescriptorPath when set.
	// Only consulted when LifecycleEnabled is true.
	DescriptorFile string
}

type OpenaceConfig struct {
	Enabled           bool
	Addr              string
	Token             string
	ProviderProfileID string
	MaxOutputLength   int
	Timeout           time.Duration
	MaxRetries        int
	RetryBaseDelay    time.Duration
	RetryMaxDelay     time.Duration
	RetryJitter       time.Duration
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
		Openace: OpenaceConfig{
			Enabled:           boolFromValue(valueFromEnv("OPENPE_OPENACE_ENABLED", fileEnv), false),
			Addr:              valueOrDefaultFromAny([]string{"OPENPE_OPENACE_ADDR", "OPENACE_DAEMON_ADDR"}, fileEnv, DefaultOpenaceAddr),
			Token:             valueFromAnyEnv([]string{"OPENPE_OPENACE_TOKEN", "OPENACE_DAEMON_TOKEN"}, fileEnv),
			ProviderProfileID: valueFromEnv("OPENPE_OPENACE_PROVIDER_PROFILE_ID", fileEnv),
			MaxOutputLength:   intFromValue(valueFromEnv("OPENPE_OPENACE_MAX_OUTPUT_LENGTH", fileEnv), DefaultOpenaceMaxOutputLength),
			Timeout:           durationFromValue(valueFromEnv("OPENPE_OPENACE_TIMEOUT", fileEnv), DefaultOpenaceTimeout),
			MaxRetries:        intFromValue(valueFromEnv("OPENPE_OPENACE_MAX_RETRIES", fileEnv), DefaultOpenaceMaxRetries),
			RetryBaseDelay:    durationFromValue(valueFromEnv("OPENPE_OPENACE_RETRY_BASE_DELAY", fileEnv), DefaultOpenaceRetryBaseDelay),
			RetryMaxDelay:     durationFromValue(valueFromEnv("OPENPE_OPENACE_RETRY_MAX_DELAY", fileEnv), DefaultOpenaceRetryMaxDelay),
			RetryJitter:       durationFromValue(valueFromEnv("OPENPE_OPENACE_RETRY_JITTER", fileEnv), DefaultOpenaceRetryJitter),
		},
		Codex: CodexConfig{
			History: CodexHistoryConfig{
				Enabled:     boolFromValue(valueFromEnv("OPENPE_CODEX_HISTORY_ENABLED", fileEnv), false),
				Home:        valueFromAnyEnv([]string{"OPENPE_CODEX_HOME", "CODEX_HOME"}, fileEnv),
				MaxMessages: intFromValue(valueFromEnv("OPENPE_CODEX_HISTORY_MAX_MESSAGES", fileEnv), DefaultCodexHistoryMaxMessages),
				MaxChars:    intFromValue(valueFromEnv("OPENPE_CODEX_HISTORY_MAX_CHARS", fileEnv), DefaultCodexHistoryMaxChars),
			},
		},
		Server: ServerConfig{
			Token:            valueFromEnv("OPENPE_SERVER_TOKEN", fileEnv),
			CORSOrigins:      splitCSV(valueFromEnv("OPENPE_SERVER_CORS_ORIGINS", fileEnv)),
			LifecycleEnabled: boolFromValue(valueFromEnv("OPENPE_SERVER_LIFECYCLE_ENABLED", fileEnv), false),
			DescriptorFile:   valueFromEnv("OPENPE_SERVER_DESCRIPTOR_FILE", fileEnv),
		},
	}
}

// splitCSV parses a comma-separated string into a clean slice, trimming
// whitespace and dropping empty entries. Returns nil when the input has no
// non-empty entries.
func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func valueFromEnv(key string, fileEnv map[string]string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value != "" {
		return value
	}
	return strings.TrimSpace(fileEnv[key])
}

func valueFromAnyEnv(keys []string, fileEnv map[string]string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	for _, key := range keys {
		if value := strings.TrimSpace(fileEnv[key]); value != "" {
			return value
		}
	}
	return ""
}

func valueOrDefault(key string, fileEnv map[string]string, fallback string) string {
	value := valueFromEnv(key, fileEnv)
	if value == "" {
		return fallback
	}
	return value
}

func valueOrDefaultFromAny(keys []string, fileEnv map[string]string, fallback string) string {
	value := valueFromAnyEnv(keys, fileEnv)
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

func intFromValue(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func boolFromValue(value string, fallback bool) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "":
		return fallback
	case "1", "true", "yes", "y", "on", "enabled":
		return true
	case "0", "false", "no", "n", "off", "disabled":
		return false
	default:
		return fallback
	}
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
