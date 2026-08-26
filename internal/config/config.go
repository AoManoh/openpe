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

	DefaultClaudeTranscriptMaxMessages = 12
	DefaultClaudeTranscriptMaxChars    = 12000

	DefaultDevinHistoryMaxMessages = 12
	DefaultDevinHistoryMaxChars    = 12000
	// DefaultDevinHistoryRecency bounds how recently the located Devin session
	// must have been active to be reused as context. The Devin UserPromptSubmit
	// hook carries no session id and the current prompt is not yet in the DB
	// when the hook fires, so devinhistory locates the session by working
	// directory + most-recent activity; this window prevents an abandoned older
	// session in the same directory from leaking stale history into a new one.
	// Raised 2h -> 6h (2026-07-02, user decision): with 2h the FIRST
	// continuation prompt after a same-day resume (e.g. a morning "继续 Phase 3"
	// on last night's session) kept missing history, because the just-submitted
	// prompt has not refreshed last_activity_at yet. 6h still guards against
	// genuinely abandoned sessions while covering same-day work gaps.
	DefaultDevinHistoryRecency = 6 * time.Hour

	// DefaultHookDedupWindow is the freshness window for the cross-adapter
	// single-flight de-duplication applied when a host agent (e.g. the Devin
	// CLI) aggregates UserPromptSubmit hooks from several ecosystems and fires
	// them all for one prompt. See internal/adapters/hookdedup.
	DefaultHookDedupWindow = 5 * time.Second
	DefaultHookDeadline    = 100 * time.Second

	// DefaultMaxContextTokens is the global token budget applied to every
	// outbound enhancer.Request via Options.MaxContextTokens. Zero means
	// "no budget" — enhancer.assemblePrompt skips its section-level
	// truncator entirely, matching the pre-budget behaviour. Setting a
	// positive value (via OPENPE_MAX_CONTEXT_TOKENS) lets every caller
	// path (codex / claude / windsurf hooks, future patch inject, future
	// IDE clients) share a single user-facing knob that controls the
	// total prompt size handed to the LLM provider, regardless of which
	// collector populated history / context.files / context.retrieval.
	DefaultMaxContextTokens = 0

	// Language guard keeps the enhanced prompt in the user's input language.
	// Enabled by default and a no-op when the languages already match (the
	// common case), so it is backward compatible. Reanchor adds one re-request
	// on a detected mismatch; disable it (OPENPE_LANGUAGE_GUARD_REANCHOR=false)
	// for latency-sensitive setups, in which case the guard only warns.
	DefaultLanguageGuardEnabled  = true
	DefaultLanguageGuardReanchor = true
)

type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	ListenAddr string
	Timeout    time.Duration
	Language   string
	// MaxContextTokens is the consumer-layer global token budget. Each
	// caller path (codex / claude / windsurf hook, future patch inject)
	// forwards this value into enhancer.Request.Options.MaxContextTokens,
	// where enhancer.assemblePrompt applies section-level truncation
	// preserving required sections (original prompt, target client,
	// workspace, enhancement contract, final instruction) and shrinking
	// optional sections (history, rules, guidelines, context.files,
	// context.retrieval). Zero (the default) keeps the historical
	// "no budget" behaviour so this field is purely additive.
	MaxContextTokens int
	// MessageStyle selects the provider message layout: "flatten" (default —
	// [system, user] with history embedded as labeled text) or "hybrid"
	// ([system, prior user/assistant turns, final user task]). Sourced from
	// OPENPE_MESSAGE_STYLE. Unknown values fall back to "flatten" so the
	// historical, eval-validated layout stays the default until hybrid is
	// promoted by eval A/B.
	MessageStyle string
	// Provider selects the model provider wire protocol: "openai" (default,
	// OpenAI-compatible /v1/chat/completions) or "anthropic" (Anthropic Messages
	// API /v1/messages). Sourced from OPENPE_PROVIDER; unknown/empty → "openai"
	// so existing setups are unaffected.
	Provider string
	// MaxTokens caps the model's response length. It is required by the
	// Anthropic provider and ignored by the OpenAI one (which lets the gateway
	// default). Sourced from OPENPE_MAX_TOKENS; 0 (default) lets the provider
	// pick its own default.
	MaxTokens int
	// SystemPrompt, when non-empty, overrides the enhancer's built-in system
	// prompt. It is populated from OPENPE_SYSTEM_PROMPT_FILE (file contents,
	// preferred) or OPENPE_SYSTEM_PROMPT (inline). Empty (the default) keeps
	// the compiled-in enhancer.defaultSystemPrompt, so this field is purely
	// additive and lets operators iterate on the prompt without recompiling.
	SystemPrompt string
	// PromptStyle selects a built-in system-prompt preset by audience:
	// "agent" (default — compact prompt for the downstream coding agent) or
	// "human" (detailed report-style expansion for human reading; the
	// former v7h default kept verbatim). Sourced from OPENPE_PROMPT_STYLE.
	// An explicit SystemPrompt overrides it. The raw value is validated at
	// service construction (enhancer.ResolveSystemPrompt), where an unknown
	// style fails startup loudly instead of silently degrading to a default.
	PromptStyle string
	// LanguageGuard configures the enhancer's post-processing language-
	// preservation guard (see internal/enhancer.LanguageGuardConfig). Sourced
	// from OPENPE_LANGUAGE_GUARD_ENABLED / OPENPE_LANGUAGE_GUARD_REANCHOR.
	LanguageGuard LanguageGuardConfig
	// Warnings configures the deterministic output-side advisory checks
	// (out-of-context numbers / undecided irreversible actions — the
	// model-independent backstop behind the v7g/v7h prompt guardrails).
	// Sourced from OPENPE_WARNINGS_ENABLED (default true),
	// OPENPE_WARNINGS_ACTIONS (comma-separated extra action words) and
	// OPENPE_WARNINGS_NUM_MAXLEN (digit-run length cap, default 5).
	Warnings WarningsConfig
	// Specs configures explicit user prompt-spec loading (`pe+<name> <task>`).
	// Dir empty means the per-user default ~/.config/openpe/specs (resolved by
	// internal/specs.DefaultDir, kept out of this package like the other
	// mirror configs); MaxChars <= 0 means the specs package default.
	Specs        SpecsConfig
	Openace      OpenaceConfig
	Codex        CodexConfig
	Claude       ClaudeConfig
	Devin        DevinConfig
	Inject       InjectConfig
	Delivery     DeliveryConfig
	Server       ServerConfig
	HookDedup    HookDedupConfig
	HookDeadline time.Duration
}

// SpecsConfig is the config-layer mirror of the internal/specs loader knobs
// (kept apart so config does not import the specs package, consistent with
// LanguageGuardConfig / WarningsConfig). Sourced from OPENPE_SPECS_DIR and
// OPENPE_SPEC_MAX_CHARS.
type SpecsConfig struct {
	Dir      string
	MaxChars int
}

// WarningsConfig is the config-layer mirror of
// enhancer.ContentWarningsConfig (kept apart so config does not import the
// enhancer package). Enabled defaults to true: the checks are advisory-only
// (never rewrite or block), so they are safe on by default.
type WarningsConfig struct {
	Enabled      bool
	ExtraActions []string
	NumMaxLen    int
}

// LanguageGuardConfig is the config-layer mirror of
// enhancer.LanguageGuardConfig. cmd maps between the two, keeping
// internal/config free of an enhancer import (consistent with MessageStyle).
type LanguageGuardConfig struct {
	Enabled  bool
	Reanchor bool
}

// HookDedupConfig controls the cross-adapter single-flight de-duplication that
// prevents a host agent which aggregates hooks from multiple ecosystems (the
// Devin CLI loads its own, Claude Code, and Windsurf hooks at once) from
// enhancing the same prompt several times. Enabled by default; the window is
// the claim freshness used by internal/adapters/hookdedup.
type HookDedupConfig struct {
	Enabled bool
	Window  time.Duration
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

type ClaudeConfig struct {
	Transcript ClaudeTranscriptConfig
}

type ClaudeTranscriptConfig struct {
	Enabled     bool
	MaxMessages int
	MaxChars    int
}

type DevinConfig struct {
	History DevinHistoryConfig
}

// DevinHistoryConfig controls reading the current Devin CLI session from its
// local SQLite store (~/.local/share/devin/cli/sessions.db) to populate
// enhancer.Request.History. DBPath empty means the devinhistory collector
// resolves the platform default. Recency bounds reuse of the located session.
type DevinHistoryConfig struct {
	Enabled     bool
	DBPath      string
	MaxMessages int
	MaxChars    int
	Recency     time.Duration
}

// InjectConfig is the resolved per-client silent-injection switch. The global
// default OPENPE_HOOK_INJECT (default false = review + clipboard, preserving
// openPE's "never auto-apply" philosophy) is overridden per client by
// OPENPE_<CLIENT>_INJECT. Windsurf cannot ingest hook-provided context, so it
// has no field here — the switch is a documented no-op there.
type InjectConfig struct {
	Codex  bool
	Claude bool
	Devin  bool
}

type DeliveryConfig struct {
	CacheDir               string
	CopyCommand            string
	DisableOSC52Clipboard  bool
	OSC52TTY               string
	ClaudePromptFallback   bool
	WindsurfPromptFallback bool
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
	// Silent-injection switch: global default + per-client override. Resolved
	// from the dotenv too (not just process env), so the hook's --env-file can
	// configure it — unlike the old os.Getenv-only OPENPE_DEVIN_INJECT read.
	globalInject := boolFromValue(valueFromEnv("OPENPE_HOOK_INJECT", fileEnv), false)
	return Config{
		BaseURL:          valueFromEnv("OPENPE_BASE_URL", fileEnv),
		APIKey:           valueFromEnv("OPENPE_API_KEY", fileEnv),
		Model:            valueFromEnv("OPENPE_MODEL", fileEnv),
		ListenAddr:       valueOrDefault("OPENPE_LISTEN_ADDR", fileEnv, DefaultListenAddr),
		Timeout:          durationFromValue(valueFromEnv("OPENPE_TIMEOUT", fileEnv), DefaultTimeout),
		Language:         normalizeLanguage(valueOrDefault("OPENPE_LANGUAGE", fileEnv, DefaultLanguage)),
		MaxContextTokens: intFromValue(valueFromEnv("OPENPE_MAX_CONTEXT_TOKENS", fileEnv), DefaultMaxContextTokens),
		MessageStyle:     normalizeMessageStyle(valueFromEnv("OPENPE_MESSAGE_STYLE", fileEnv)),
		Provider:         normalizeProvider(valueFromEnv("OPENPE_PROVIDER", fileEnv)),
		MaxTokens:        intFromValue(valueFromEnv("OPENPE_MAX_TOKENS", fileEnv), 0),
		SystemPrompt:     systemPromptFromEnv(fileEnv),
		PromptStyle:      valueFromEnv("OPENPE_PROMPT_STYLE", fileEnv),
		LanguageGuard: LanguageGuardConfig{
			Enabled:  boolFromValue(valueFromEnv("OPENPE_LANGUAGE_GUARD_ENABLED", fileEnv), DefaultLanguageGuardEnabled),
			Reanchor: boolFromValue(valueFromEnv("OPENPE_LANGUAGE_GUARD_REANCHOR", fileEnv), DefaultLanguageGuardReanchor),
		},
		Warnings: WarningsConfig{
			Enabled:      boolFromValue(valueFromEnv("OPENPE_WARNINGS_ENABLED", fileEnv), true),
			ExtraActions: splitCSV(valueFromEnv("OPENPE_WARNINGS_ACTIONS", fileEnv)),
			NumMaxLen:    intFromValue(valueFromEnv("OPENPE_WARNINGS_NUM_MAXLEN", fileEnv), 5),
		},
		Specs: SpecsConfig{
			Dir:      valueFromEnv("OPENPE_SPECS_DIR", fileEnv),
			MaxChars: intFromValue(valueFromEnv("OPENPE_SPEC_MAX_CHARS", fileEnv), 0),
		},
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
				// Default true: enable full prompt-enhancement context by reading
				// the current Codex session history. The codexhistory provider
				// silently skips when ~/.codex/history.jsonl / rollout file is
				// missing or cwd mismatches, so this is safe by default.
				Enabled:     boolFromValue(valueFromEnv("OPENPE_CODEX_HISTORY_ENABLED", fileEnv), true),
				Home:        valueFromAnyEnv([]string{"OPENPE_CODEX_HOME", "CODEX_HOME"}, fileEnv),
				MaxMessages: intFromValue(valueFromEnv("OPENPE_CODEX_HISTORY_MAX_MESSAGES", fileEnv), DefaultCodexHistoryMaxMessages),
				MaxChars:    intFromValue(valueFromEnv("OPENPE_CODEX_HISTORY_MAX_CHARS", fileEnv), DefaultCodexHistoryMaxChars),
			},
		},
		Claude: ClaudeConfig{
			Transcript: ClaudeTranscriptConfig{
				// Default true: enable full prompt-enhancement context by reading
				// Claude Code's transcript_path. The claudetranscript provider
				// silently skips when transcript is missing or cwd mismatches,
				// so this is safe by default.
				Enabled:     boolFromValue(valueFromEnv("OPENPE_CLAUDE_TRANSCRIPT_ENABLED", fileEnv), true),
				MaxMessages: intFromValue(valueFromEnv("OPENPE_CLAUDE_TRANSCRIPT_MAX_MESSAGES", fileEnv), DefaultClaudeTranscriptMaxMessages),
				MaxChars:    intFromValue(valueFromEnv("OPENPE_CLAUDE_TRANSCRIPT_MAX_CHARS", fileEnv), DefaultClaudeTranscriptMaxChars),
			},
		},
		Devin: DevinConfig{
			History: DevinHistoryConfig{
				// Default true: like codex/claude, read the current Devin session
				// for context. The devinhistory provider only ever reads the
				// SQLite store read-only; when the DB / session is absent it
				// reports an explicit "no history" status (surfaced, not silent).
				Enabled:     boolFromValue(valueFromEnv("OPENPE_DEVIN_HISTORY_ENABLED", fileEnv), true),
				DBPath:      valueFromEnv("OPENPE_DEVIN_HISTORY_DB_PATH", fileEnv),
				MaxMessages: intFromValue(valueFromEnv("OPENPE_DEVIN_HISTORY_MAX_MESSAGES", fileEnv), DefaultDevinHistoryMaxMessages),
				MaxChars:    intFromValue(valueFromEnv("OPENPE_DEVIN_HISTORY_MAX_CHARS", fileEnv), DefaultDevinHistoryMaxChars),
				Recency:     durationFromValue(valueFromEnv("OPENPE_DEVIN_HISTORY_RECENCY", fileEnv), DefaultDevinHistoryRecency),
			},
		},
		Inject: InjectConfig{
			Codex:  boolFromValue(valueFromEnv("OPENPE_CODEX_INJECT", fileEnv), globalInject),
			Claude: boolFromValue(valueFromEnv("OPENPE_CLAUDE_INJECT", fileEnv), globalInject),
			Devin:  boolFromValue(valueFromEnv("OPENPE_DEVIN_INJECT", fileEnv), globalInject),
		},
		Delivery: DeliveryConfig{
			CacheDir:               valueFromEnv("OPENPE_CACHE_DIR", fileEnv),
			CopyCommand:            valueFromEnv("OPENPE_COPY_COMMAND", fileEnv),
			DisableOSC52Clipboard:  boolFromValue(valueFromEnv("OPENPE_DISABLE_OSC52_CLIPBOARD", fileEnv), false),
			OSC52TTY:               valueFromEnv("OPENPE_OSC52_TTY", fileEnv),
			ClaudePromptFallback:   boolFromValue(valueFromEnv("OPENPE_CLAUDE_PROMPT_FALLBACK", fileEnv), true),
			WindsurfPromptFallback: boolFromValue(valueFromEnv("OPENPE_WINDSURF_PROMPT_FALLBACK", fileEnv), false),
		},
		Server: ServerConfig{
			Token:            valueFromEnv("OPENPE_SERVER_TOKEN", fileEnv),
			CORSOrigins:      splitCSV(valueFromEnv("OPENPE_SERVER_CORS_ORIGINS", fileEnv)),
			LifecycleEnabled: boolFromValue(valueFromEnv("OPENPE_SERVER_LIFECYCLE_ENABLED", fileEnv), false),
			DescriptorFile:   valueFromEnv("OPENPE_SERVER_DESCRIPTOR_FILE", fileEnv),
		},
		HookDedup: HookDedupConfig{
			// Default true: a single prompt submitted inside a hook-aggregating
			// host (Devin) must be enhanced exactly once even when openPE hooks
			// are installed for several clients at the same time.
			Enabled: boolFromValue(valueFromEnv("OPENPE_HOOK_DEDUP_ENABLED", fileEnv), true),
			Window:  durationFromValue(valueFromEnv("OPENPE_HOOK_DEDUP_WINDOW", fileEnv), DefaultHookDedupWindow),
		},
		HookDeadline: durationFromValue(valueFromEnv("OPENPE_HOOK_DEADLINE", fileEnv), DefaultHookDeadline),
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

// systemPromptFromEnv resolves an optional system-prompt override. A file path
// (OPENPE_SYSTEM_PROMPT_FILE) takes precedence over an inline value
// (OPENPE_SYSTEM_PROMPT) so deployments can ship a prompt file and iterate on it
// without restarting via env edits. A missing/unreadable/empty file falls back
// to the inline value, then to "" (meaning: keep the built-in default). The
// fallback is intentionally lenient to match the rest of this best-effort loader.
func systemPromptFromEnv(fileEnv map[string]string) string {
	if path := valueFromEnv("OPENPE_SYSTEM_PROMPT_FILE", fileEnv); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			if s := strings.TrimSpace(string(data)); s != "" {
				return s
			}
		}
	}
	return valueFromEnv("OPENPE_SYSTEM_PROMPT", fileEnv)
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

// normalizeMessageStyle maps OPENPE_MESSAGE_STYLE to "flatten" (default),
// "hybrid", or "structured". Unknown/empty values fall back to "flatten" so the
// eval-validated layout stays the default until hybrid/structured are
// explicitly promoted. "structured" adds a separated read-only reference block
// on top of the hybrid multi-turn layout (see enhancer.StyleStructured).
func normalizeMessageStyle(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "hybrid", "multi-turn", "multiturn":
		return "hybrid"
	case "structured", "structured-v2", "structuredv2":
		return "structured"
	default:
		return "flatten"
	}
}

// normalizeProvider maps OPENPE_PROVIDER to "anthropic" or "openai" (default).
// Unknown/empty values fall back to "openai" so existing setups keep working.
func normalizeProvider(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "anthropic", "claude", "messages":
		return "anthropic"
	default:
		return "openai"
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
