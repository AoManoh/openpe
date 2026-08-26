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
	"github.com/AoManoh/openpe/internal/specs"
	"github.com/AoManoh/openpe/internal/update"
	"github.com/AoManoh/openpe/internal/version"
	"github.com/AoManoh/openpe/internal/wiring"
)

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
	// 后台版本检查只在真实二进制入口启用：测试直接调用各 runner 时保持
	// 关闭，防止 go test 二进制被当作 openpe 反复自我拉起。
	updateRefreshEnabled = true
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
	case "update":
		return runUpdate(args[1:], stdout, stderr, runCmd)
	case "-h", "--help", "help":
		printUsage(stdout)
		return 0
	case "-v", "--version", "version":
		fmt.Fprintf(stdout, "openpe %s\n", version.Value())
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
	var specNames repeatedFlag
	fs.Var(&specNames, "spec", "user spec name to load and append verbatim (repeatable; resolved in OPENPE_SPECS_DIR, default ~/.config/openpe/specs)")
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
	// 用户点名的规范在调用模型之前解析：失败立即退出（零 token 消耗），
	// 不存在"增强了但没带规范"的静默输出。
	loadedSpecs, specErr := specs.LoadWithDefaults(cfg.Specs.Dir, specNames, cfg.Specs.MaxChars)
	if specErr != nil {
		fmt.Fprintln(stderr, specs.ErrorMessage(specErr, cfg.Language))
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
	// a1 机械追加：交付文本（纯文本与 JSON 的 enhanced_prompt 字段一致）
	// 均为"增强正文 + 规范原文块"。
	resp.EnhancedPrompt = specs.Append(resp.EnhancedPrompt, loadedSpecs, cfg.Language)
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
	fmt.Fprintln(w, "  openpe update [--check]")
	fmt.Fprintln(w, "  openpe --version")
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

// disclosureNotes joins the history disclosure, the applied user specs, and
// the enhancer's advisory warnings (out-of-context numbers / undecided
// irreversible actions / language mismatch) into the single non-silent prefix
// every hook shows the user. Warnings exist precisely to be read BEFORE the
// user acts on the enhanced prompt (b3645b1: three real fabrication
// incidents), so every formal hook path — codex, claude, windsurf and devin
// alike — must surface them, not just the JSON/HTTP callers.
func disclosureNotes(messages []enhancer.Message, status histstatus.Status, summaries int, histErr error, warnings []string, appliedSpecs []string, updateNotice string, language string) string {
	notes := make([]string, 0, 3+len(warnings))
	if note := historyDisclosure(messages, status, summaries, histErr, language); note != "" {
		notes = append(notes, note)
	}
	if note := specsDisclosure(appliedSpecs, language); note != "" {
		notes = append(notes, note)
	}
	notes = append(notes, warnings...)
	// 新版提醒放最后：它是最低优先级的辅助信息，不得遮蔽内容警告。
	if updateNotice != "" {
		notes = append(notes, updateNotice)
	}
	return strings.Join(notes, " ")
}

// specsDisclosure names the user specs appended to this enhancement
// (`pe+<name>`), so the user can tell at a glance that their named specs were
// actually applied. Empty when no spec was named — silence is correct there.
func specsDisclosure(names []string, language string) string {
	if len(names) == 0 {
		return ""
	}
	if isEnglishLanguage(language) {
		return fmt.Sprintf("openPE: applied user spec(s): %s.", strings.Join(names, ", "))
	}
	return fmt.Sprintf("openPE：已应用规范：%s。", strings.Join(names, "、"))
}

// updateRefreshEnabled gates the detached background version check. Only the
// real binary entrypoint (main) enables it; tests that call the hook runners
// directly keep it off so the go-test binary is never re-launched as openpe.
var updateRefreshEnabled = false

// startDetachedCommand launches a fire-and-forget child process; swapped in
// tests. The child is not waited on — it outlives the short-lived hook.
var startDetachedCommand = func(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Start()
}

// maybeStartUpdateRefresh spawns `openpe update --refresh-cache` as a detached
// child when the notice cache is missing or stale (business contract U2.3).
// It never runs on the enhancement critical path itself — the child talks to
// the module proxy while the hook goes on with its own work — and it is a
// no-op when the notice is disabled, in CI, or the cache is still fresh.
func maybeStartUpdateRefresh(cfg config.Config) {
	if !updateRefreshEnabled || !cfg.Update.Notice || os.Getenv("CI") != "" {
		return
	}
	path, err := update.StatePath(cfg.Delivery.CacheDir)
	if err != nil {
		return
	}
	if state, ok := update.LoadState(path); ok && state.Fresh(time.Now(), cfg.Update.CheckInterval) {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	_ = startDetachedCommand(exe, "update", "--refresh-cache")
}

// updateDisclosure renders the one-line new-release notice for the hook
// disclosure prefix (business contract U2.1). It only ever reads the local
// cache — never the network — and stays silent when the cache is missing,
// stale, not newer, or the current version cannot be compared (devel).
func updateDisclosure(cfg config.Config, current string, language string) string {
	if !cfg.Update.Notice {
		return ""
	}
	path, err := update.StatePath(cfg.Delivery.CacheDir)
	if err != nil {
		return ""
	}
	state, ok := update.LoadState(path)
	latest, notify := update.NoticeVersion(state, ok, current, time.Now(), cfg.Update.CheckInterval)
	if !notify {
		return ""
	}
	if isEnglishLanguage(language) {
		return fmt.Sprintf("openPE: new release %s available (current %s); run `openpe update` to upgrade.", latest, current)
	}
	return fmt.Sprintf("openPE：发现新版本 %s（当前 %s），运行 openpe update 升级。", latest, current)
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
