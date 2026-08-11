package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/AoManoh/openpe/internal/config"
	"github.com/AoManoh/openpe/internal/context/histstatus"
	"github.com/AoManoh/openpe/internal/enhancer"
	"github.com/AoManoh/openpe/internal/providers"
	"github.com/AoManoh/openpe/internal/wiring"
)

// Version is the build identifier exposed via `openpe --version`. The
// default "dev" matches `go install ./cmd/openpe` users who do not pass
// ldflags; release builds should override it with the git tag / commit:
//
//	go build -ldflags "-X main.Version=v0.2.0" ./cmd/openpe
var Version = "dev"

type providerFactory func(providers.Spec) (enhancer.Provider, error)
type commandRunner func(ctx context.Context, name string, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error

type providerOptions struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
}

type providerFlagValues struct {
	baseURL *configStringValue
	apiKey  *configStringValue
	model   *configStringValue
	timeout *time.Duration
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, func(s providers.Spec) (enhancer.Provider, error) {
		return providers.New(s)
	}, os.Getwd, runCommand))
}

func run(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error), runCmd commandRunner) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
	case "enhance":
		return runEnhance(args[1:], stdin, stdout, stderr, newProvider, getwd)
	case "codex":
		return runCodex(args[1:], stdin, stdout, stderr, newProvider, getwd, runCmd)
	case "claude":
		return runClaude(args[1:], stdin, stdout, stderr, newProvider, getwd)
	case "windsurf":
		return runWindsurf(args[1:], stdin, stdout, stderr, newProvider, getwd)
	case "devin":
		return runDevin(args[1:], stdin, stdout, stderr, newProvider, getwd)
	case "-h", "--help", "help":
		printUsage(stdout)
		return 0
	case "-v", "--version", "version":
		fmt.Fprintf(stdout, "openpe %s\n", Version)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func runEnhance(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error)) int {
	cfg := config.Load()
	fs := flag.NewFlagSet("enhance", flag.ContinueOnError)
	fs.SetOutput(stderr)
	prompt := fs.String("prompt", "", "raw prompt to enhance; stdin is used when omitted")
	jsonOutput := fs.Bool("json", false, "write full JSON response")
	client := fs.String("client", "generic", "target client name")
	mode := fs.String("mode", "agent", "prompt mode")
	cwd := fs.String("cwd", "", "workspace path")
	baseURL := configStringFlag(fs, "base-url", "OpenAI-compatible base URL (defaults to OPENPE_BASE_URL)")
	apiKey := configStringFlag(fs, "api-key", "OpenAI-compatible API key (defaults to OPENPE_API_KEY)")
	model := configStringFlag(fs, "model", "OpenAI-compatible model (defaults to OPENPE_MODEL)")
	timeout := fs.Duration("timeout", cfg.Timeout, "provider timeout")
	if ok, code := parseFlagSet(fs, args); !ok {
		return code
	}
	rawPrompt := strings.TrimSpace(*prompt)
	if rawPrompt == "" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			fmt.Fprintf(stderr, "read stdin: %v\n", err)
			return 1
		}
		rawPrompt = strings.TrimSpace(string(data))
	}
	if rawPrompt == "" {
		fmt.Fprintln(stderr, "prompt is required")
		return 1
	}
	if *cwd == "" {
		workingDir, err := getwd()
		if err != nil {
			fmt.Fprintf(stderr, "get cwd: %v\n", err)
			return 1
		}
		*cwd = workingDir
	}
	provider, err := newProvider(providers.Spec{
		Provider:  cfg.Provider,
		MaxTokens: cfg.MaxTokens,
		BaseURL:   baseURL.ValueOrDefault(cfg.BaseURL),
		APIKey:    apiKey.ValueOrDefault(cfg.APIKey),
		Model:     model.ValueOrDefault(cfg.Model),
		Timeout:   *timeout,
	})
	if err != nil {
		fmt.Fprintf(stderr, "configure provider: %v\n", err)
		return 1
	}
	service, err := newEnhancerService(provider, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "configure enhancer: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeoutOrDefault(*timeout))
	defer cancel()
	resp, err := service.Enhance(ctx, enhancer.Request{
		Prompt:  rawPrompt,
		Client:  *client,
		CWD:     *cwd,
		Mode:    *mode,
		Options: enhancer.Options{MaxContextTokens: cfg.MaxContextTokens},
	})
	if err != nil {
		fmt.Fprintf(stderr, "enhance prompt: %v\n", err)
		return 1
	}
	if *jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(resp); err != nil {
			fmt.Fprintf(stderr, "write json: %v\n", err)
			return 1
		}
		return 0
	}
	fmt.Fprintln(stdout, resp.EnhancedPrompt)
	return 0
}

func bindProviderFlags(fs *flag.FlagSet, cfg config.Config) providerFlagValues {
	return providerFlagValues{
		baseURL: configStringFlag(fs, "base-url", "OpenAI-compatible base URL (defaults to OPENPE_BASE_URL)"),
		apiKey:  configStringFlag(fs, "api-key", "OpenAI-compatible API key (defaults to OPENPE_API_KEY)"),
		model:   configStringFlag(fs, "model", "OpenAI-compatible model (defaults to OPENPE_MODEL)"),
		timeout: fs.Duration("timeout", cfg.Timeout, "provider timeout"),
	}
}

func (values providerFlagValues) options(cfg config.Config) providerOptions {
	return providerOptions{
		BaseURL: values.baseURL.ValueOrDefault(cfg.BaseURL),
		APIKey:  values.apiKey.ValueOrDefault(cfg.APIKey),
		Model:   values.model.ValueOrDefault(cfg.Model),
		Timeout: *values.timeout,
	}
}

func newConfiguredEnhancerService(newProvider providerFactory, cfg config.Config, opts providerOptions) (*enhancer.Service, error) {
	provider, err := newProvider(providers.Spec{
		Provider:  cfg.Provider,
		MaxTokens: cfg.MaxTokens,
		BaseURL:   opts.BaseURL,
		APIKey:    opts.APIKey,
		Model:     opts.Model,
		Timeout:   opts.Timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("configure provider: %w", err)
	}
	service, err := newEnhancerService(provider, cfg)
	if err != nil {
		return nil, fmt.Errorf("configure enhancer: %w", err)
	}
	return service, nil
}

func workingDirectory(value string, getwd func() (string, error)) (string, error) {
	if value != "" {
		return value, nil
	}
	return getwd()
}

func runCommand(ctx context.Context, name string, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// newEnhancerService delegates to the shared wiring builder so the CLI/hook
// entry can never drift from openpe-server again (CR-009).
func newEnhancerService(provider enhancer.Provider, cfg config.Config) (*enhancer.Service, error) {
	return wiring.NewEnhancerService(provider, cfg)
}

func timeoutOrDefault(value time.Duration) time.Duration {
	if value <= 0 {
		return config.DefaultTimeout
	}
	return value
}

type configStringValue struct {
	value string
	set   bool
}

func configStringFlag(fs *flag.FlagSet, name string, usage string) *configStringValue {
	value := &configStringValue{}
	fs.Var(value, name, usage)
	return value
}

func (v *configStringValue) String() string {
	if v == nil {
		return ""
	}
	return v.value
}

func (v *configStringValue) Set(value string) error {
	v.value = value
	v.set = true
	return nil
}

func (v *configStringValue) ValueOrDefault(defaultValue string) string {
	if v != nil && v.set {
		return strings.TrimSpace(v.value)
	}
	return strings.TrimSpace(defaultValue)
}

func parseFlagSet(fs *flag.FlagSet, args []string) (bool, int) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return false, 0
		}
		return false, 2
	}
	return true, 0
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "正式使用：安装 hook 后，在 Codex、Claude Code、Devin CLI 或 Windsurf Cascade 对话终端输入 `pe <内容>`。")
	fmt.Fprintln(w, "测试/调试命令：")
	fmt.Fprintln(w, "  openpe codex hook install [--scope project|user]")
	fmt.Fprintln(w, "  openpe claude hook install")
	fmt.Fprintln(w, "  openpe devin hook install [--scope project|user]")
	fmt.Fprintln(w, "  openpe windsurf hook install [--scope project|user]")
	fmt.Fprintln(w, "  openpe enhance [--prompt text] [--json] [--client name] [--mode name]")
	fmt.Fprintln(w, "  openpe codex [--prompt text] [--dry-run] [--codex-arg arg]...")
	fmt.Fprintln(w, "  openpe codex hook run")
	fmt.Fprintln(w, "  openpe codex last [--path] [--prompt]")
	fmt.Fprintln(w, "  openpe claude hook run")
	fmt.Fprintln(w, "  openpe claude hook last [--path] [--prompt]")
	fmt.Fprintln(w, "  openpe devin hook run")
	fmt.Fprintln(w, "  openpe devin hook last [--path] [--prompt]")
	fmt.Fprintln(w, "  openpe windsurf hook run")
	fmt.Fprintln(w, "  openpe windsurf hook last [--path] [--prompt]")
}

func printCodexHookUsage(w io.Writer) {
	fmt.Fprintln(w, "正式使用：安装 hook 后，在 Codex 对话终端输入 `pe <内容>`。")
	fmt.Fprintln(w, "测试/调试命令：")
	fmt.Fprintln(w, "  openpe codex hook install [--scope project|user] [--path hooks.json] [--openpe-bin path]")
	fmt.Fprintln(w, "  openpe codex hook run [--auto] [--block-output json|stderr] [--copy-preview] [--terminal-preview=false] [--hook-scope user|project]")
	fmt.Fprintln(w, "  openpe codex hook last [--path] [--prompt]")
}

func printClaudeHookUsage(w io.Writer) {
	fmt.Fprintln(w, "正式使用：安装 hook 后，在 Claude Code 对话终端输入 `pe <内容>`。")
	fmt.Fprintln(w, "测试/调试命令：")
	fmt.Fprintln(w, "  openpe claude hook install [--path settings.json] [--openpe-bin path]")
	fmt.Fprintln(w, "  openpe claude hook run")
	fmt.Fprintln(w, "  openpe claude hook last [--path] [--prompt]")
}

func printWindsurfHookUsage(w io.Writer) {
	fmt.Fprintln(w, "正式使用：安装 hook 后，在 Windsurf Cascade 输入 `pe <内容>`。")
	fmt.Fprintln(w, "测试/调试命令：")
	fmt.Fprintln(w, "  openpe windsurf hook install [--scope project|user] [--path hooks.json] [--openpe-bin path]")
	fmt.Fprintln(w, "  openpe windsurf hook run")
	fmt.Fprintln(w, "  openpe windsurf hook last [--path] [--prompt]")
}

func printDevinHookUsage(w io.Writer) {
	fmt.Fprintln(w, "正式使用：安装 hook 后，在 Devin CLI 对话终端输入 `pe <内容>`。")
	fmt.Fprintln(w, "测试/调试命令：")
	fmt.Fprintln(w, "  openpe devin hook install [--scope project|user] [--path <file>] [--openpe-bin path]")
	fmt.Fprintln(w, "  openpe devin hook run")
	fmt.Fprintln(w, "  openpe devin hook last [--path] [--prompt]")
}

func localizedInvalidDevinHookInput(err error, language string) string {
	if isEnglishLanguage(language) {
		return fmt.Sprintf("openPE skipped prompt enhancement: invalid Devin hook input: %v", err)
	}
	return fmt.Sprintf("openPE 跳过增强：Devin hook 输入无效：%v", err)
}

func envOrDefault(name string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func envBoolOrDefault(name string, fallback bool) bool {
	switch strings.TrimSpace(strings.ToLower(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func localizedInvalidCodexHookInput(err error, language string) string {
	if isEnglishLanguage(language) {
		return fmt.Sprintf("openPE skipped prompt enhancement: invalid hook input: %v", err)
	}
	return fmt.Sprintf("openPE 跳过增强：hook 输入无效：%v", err)
}

func localizedInvalidClaudeHookInput(err error, language string) string {
	if isEnglishLanguage(language) {
		return fmt.Sprintf("openPE skipped prompt enhancement: invalid Claude hook input: %v", err)
	}
	return fmt.Sprintf("openPE 跳过增强：Claude hook 输入无效：%v", err)
}

func localizedInvalidWindsurfHookInput(err error, language string) string {
	if isEnglishLanguage(language) {
		return fmt.Sprintf("openPE skipped prompt enhancement: invalid Windsurf hook input: %v", err)
	}
	return fmt.Sprintf("openPE 跳过增强：Windsurf hook 输入无效：%v", err)
}

// localizedHistoryReadFailure is shown when conversation-history reading was
// enabled but genuinely failed. It makes the degraded result explicit instead
// of silently producing a history-less enhancement the user assumes had history.
func localizedHistoryReadFailure(err error, language string) string {
	if isEnglishLanguage(language) {
		return fmt.Sprintf("openPE warning: failed to read conversation history (%v); this enhancement does NOT include history context.", err)
	}
	return fmt.Sprintf("openPE 提示：读取历史上下文失败（%v），本次增强未包含会话历史。", err)
}

// historyDisclosure renders the single, always-visible line about prior-context
// for one enhancement. openPE never silently falls back to a history-less
// enhancement: a genuine read failure is reported as a failure, every
// not-included reason is named, and a successful include states the count
// (noting when it contains a compaction summary — right after a compaction the
// summary is the only prior context, and the user should know it was carried
// over rather than lost). summaries is 0 for collectors that cannot identify
// summaries (codex/claude/windsurf). An empty result means "say nothing" and
// occurs only when the history feature is disabled (Unknown) — i.e. the user
// opted out, so silence is correct.
func historyDisclosure(messages []enhancer.Message, status histstatus.Status, summaries int, histErr error, language string) string {
	if histErr != nil {
		return localizedHistoryReadFailure(histErr, language)
	}
	return localizedHistoryNote(status, len(messages), summaries, language)
}

// disclosureNotes joins the history disclosure with the enhancer's advisory
// warnings (out-of-context numbers / undecided irreversible actions / language
// mismatch) into the single non-silent prefix every hook shows the user.
// Warnings exist precisely to be read BEFORE the user acts on the enhanced
// prompt (b3645b1: three real fabrication incidents), so every formal hook
// path — codex, claude, windsurf and devin alike — must surface them, not
// just the JSON/HTTP callers.
func disclosureNotes(messages []enhancer.Message, status histstatus.Status, summaries int, histErr error, warnings []string, language string) string {
	notes := make([]string, 0, 1+len(warnings))
	if note := historyDisclosure(messages, status, summaries, histErr, language); note != "" {
		notes = append(notes, note)
	}
	notes = append(notes, warnings...)
	return strings.Join(notes, " ")
}

func localizedHistoryNote(status histstatus.Status, count int, summaries int, language string) string {
	en := isEnglishLanguage(language)
	switch status {
	case histstatus.Found:
		if summaries > 0 {
			if en {
				return fmt.Sprintf("openPE: included %d prior message(s) (including the compacted-conversation summary) as conversation context.", count)
			}
			return fmt.Sprintf("openPE：已带入 %d 条会话历史（含前文压缩摘要）作为上下文。", count)
		}
		if en {
			return fmt.Sprintf("openPE: included %d prior message(s) as conversation context.", count)
		}
		return fmt.Sprintf("openPE：已带入 %d 条会话历史作为上下文。", count)
	case histstatus.NoSession:
		if en {
			return "openPE: no prior conversation context found for this workspace; enhanced without history."
		}
		return "openPE：未找到本工作区的会话历史，本次未带前文上下文。"
	case histstatus.Empty:
		if en {
			return "openPE: the located session had no reusable conversation; enhanced without history."
		}
		return "openPE：会话历史为空，本次未带前文上下文。"
	case histstatus.Stale:
		if en {
			return "openPE: the most recent session is outside the freshness window; enhanced without history."
		}
		return "openPE：最近会话已超出时效窗口，本次未带前文上下文。"
	case histstatus.CWDMismatch:
		if en {
			return "openPE: the located session belongs to another workspace; enhanced without history."
		}
		return "openPE：会话历史属于其它工作区，本次未带前文上下文。"
	case histstatus.Ambiguous:
		if en {
			return "openPE: multiple sessions are active in this directory and the current one could not be identified; enhanced without history to avoid injecting another conversation's context."
		}
		return "openPE：本目录存在多个活跃会话、无法确定当前会话，本次未带前文上下文（避免串入其它会话内容）。"
	default:
		// Unknown: the history feature is disabled — make no claim.
		return ""
	}
}

func localizedEnhanceFailure(message string, language string) string {
	if isEnglishLanguage(language) {
		return "openPE prompt enhancement failed: " + message
	}
	return "openPE 增强失败：" + message
}

func isEnglishLanguage(language string) bool {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "en", "en-us", "english":
		return true
	default:
		return false
	}
}

type repeatedFlag []string

func (f *repeatedFlag) String() string {
	return strings.Join(*f, " ")
}

func (f *repeatedFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}
