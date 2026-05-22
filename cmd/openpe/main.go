package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	claudeadapter "github.com/AoManoh/openpe/internal/adapters/claude"
	codexadapter "github.com/AoManoh/openpe/internal/adapters/codex"
	"github.com/AoManoh/openpe/internal/adapters/delivery"
	windsurfadapter "github.com/AoManoh/openpe/internal/adapters/windsurf"
	"github.com/AoManoh/openpe/internal/config"
	claudetranscript "github.com/AoManoh/openpe/internal/context/claudetranscript"
	codexhistory "github.com/AoManoh/openpe/internal/context/codexhistory"
	openacectx "github.com/AoManoh/openpe/internal/context/openace"
	"github.com/AoManoh/openpe/internal/enhancer"
	"github.com/AoManoh/openpe/internal/providers/openai"
)

type providerFactory func(openai.Config) (enhancer.Provider, error)
type commandRunner func(ctx context.Context, name string, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, func(cfg openai.Config) (enhancer.Provider, error) {
		return openai.New(cfg)
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
	case "-h", "--help", "help":
		printUsage(stdout)
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
	provider, err := newProvider(openai.Config{
		BaseURL: baseURL.ValueOrDefault(cfg.BaseURL),
		APIKey:  apiKey.ValueOrDefault(cfg.APIKey),
		Model:   model.ValueOrDefault(cfg.Model),
		Timeout: *timeout,
	})
	if err != nil {
		fmt.Fprintf(stderr, "configure provider: %v\n", err)
		return 1
	}
	service, err := newEnhancerService(provider, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "configure context provider: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeoutOrDefault(*timeout))
	defer cancel()
	resp, err := service.Enhance(ctx, enhancer.Request{
		Prompt: rawPrompt,
		Client: *client,
		CWD:    *cwd,
		Mode:   *mode,
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

func runCodex(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error), runCmd commandRunner) int {
	if len(args) > 0 && args[0] == "hook" {
		return runCodexHook(args[1:], stdin, stdout, stderr, newProvider, getwd)
	}
	if len(args) > 0 && args[0] == "last" {
		return runCodexHookLast(args[1:], stdout, stderr)
	}
	cfg := config.Load()
	fs := flag.NewFlagSet("codex", flag.ContinueOnError)
	fs.SetOutput(stderr)
	prompt := fs.String("prompt", "", "raw prompt to enhance; stdin or positional args are used when omitted")
	dryRun := fs.Bool("dry-run", false, "print the enhanced prompt without invoking codex")
	client := fs.String("client", "codex", "target client name")
	mode := fs.String("mode", "agent", "prompt mode")
	cwd := fs.String("cwd", "", "workspace path passed to codex exec -C")
	codexBin := fs.String("codex-bin", envOrDefault("CODEX_BIN", "codex"), "codex executable path")
	var codexArgs repeatedFlag
	fs.Var(&codexArgs, "codex-arg", "extra argument passed to codex exec; repeat for multiple args")
	baseURL := configStringFlag(fs, "base-url", "OpenAI-compatible base URL (defaults to OPENPE_BASE_URL)")
	apiKey := configStringFlag(fs, "api-key", "OpenAI-compatible API key (defaults to OPENPE_API_KEY)")
	model := configStringFlag(fs, "model", "OpenAI-compatible model (defaults to OPENPE_MODEL)")
	timeout := fs.Duration("timeout", cfg.Timeout, "provider timeout")
	if ok, code := parseFlagSet(fs, args); !ok {
		return code
	}
	rawPrompt := strings.TrimSpace(*prompt)
	if rawPrompt == "" && len(fs.Args()) > 0 {
		rawPrompt = strings.TrimSpace(strings.Join(fs.Args(), " "))
	}
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
	provider, err := newProvider(openai.Config{
		BaseURL: baseURL.ValueOrDefault(cfg.BaseURL),
		APIKey:  apiKey.ValueOrDefault(cfg.APIKey),
		Model:   model.ValueOrDefault(cfg.Model),
		Timeout: *timeout,
	})
	if err != nil {
		fmt.Fprintf(stderr, "configure provider: %v\n", err)
		return 1
	}
	service, err := newEnhancerService(provider, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "configure context provider: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeoutOrDefault(*timeout))
	defer cancel()
	resp, err := service.Enhance(ctx, enhancer.Request{
		Prompt: rawPrompt,
		Client: *client,
		CWD:    *cwd,
		Mode:   *mode,
	})
	if err != nil {
		fmt.Fprintf(stderr, "enhance prompt: %v\n", err)
		return 1
	}
	if *dryRun {
		fmt.Fprintln(stdout, resp.EnhancedPrompt)
		return 0
	}
	commandArgs := []string{"exec", "-C", *cwd}
	commandArgs = append(commandArgs, codexArgs...)
	commandArgs = append(commandArgs, "-")
	if err := runCmd(context.Background(), *codexBin, commandArgs, strings.NewReader(resp.EnhancedPrompt+"\n"), stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "run codex: %v\n", err)
		return 1
	}
	return 0
}

func runCodexHook(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error)) int {
	if len(args) == 0 {
		printCodexHookUsage(stderr)
		return 2
	}
	switch args[0] {
	case "run":
		return runCodexHookRun(args[1:], stdin, stdout, stderr, newProvider, getwd)
	case "install":
		return runCodexHookInstall(args[1:], stdout, stderr, getwd)
	case "last":
		return runCodexHookLast(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		printCodexHookUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown codex hook command: %s\n", args[0])
		printCodexHookUsage(stderr)
		return 2
	}
}

func runCodexHookLast(args []string, stdout io.Writer, stderr io.Writer) int {
	return runDeliveryLast("codex hook last", "codex", args, stdout, stderr)
}

func runDeliveryLast(commandName string, client string, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet(commandName, flag.ContinueOnError)
	fs.SetOutput(stderr)
	pathOnly := fs.Bool("path", false, "print the cached content path")
	promptOnly := fs.Bool("prompt", false, "print the paste-ready enhanced prompt instead of Markdown preview")
	if ok, code := parseFlagSet(fs, args); !ok {
		return code
	}
	if *pathOnly {
		pathFn := delivery.LastPreviewPath
		if *promptOnly {
			pathFn = delivery.LastPromptPath
		}
		path, err := pathFn(client)
		if err != nil {
			fmt.Fprintf(stderr, "resolve cache path: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, path)
		return 0
	}
	readFn := delivery.ReadLastPreview
	if *promptOnly {
		readFn = delivery.ReadLastPrompt
	}
	content, err := readFn(client)
	if err != nil {
		fmt.Fprintf(stderr, "read cached content: %v\n", err)
		return 1
	}
	fmt.Fprint(stdout, content)
	return 0
}

func runCodexHookRun(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error)) int {
	cfg := config.Load()
	fs := flag.NewFlagSet("codex hook run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	client := fs.String("client", "codex", "target client name")
	mode := fs.String("mode", "agent", "prompt mode")
	auto := fs.Bool("auto", false, "enhance every prompt and inject it as additional context")
	blockOutput := fs.String("block-output", "json", "preview block output: json or stderr")
	terminalPreview := fs.Bool("terminal-preview", envBoolOrDefault("OPENPE_CODEX_TERMINAL_PREVIEW", false), "experimental: write full Markdown preview directly to /dev/tty when blocking")
	copyPreview := fs.Bool("copy-preview", envBoolOrDefault("OPENPE_CODEX_COPY_PREVIEW", false), "copy enhanced prompt to the system clipboard when blocking")
	hookScope := fs.String("hook-scope", envOrDefault("OPENPE_HOOK_SCOPE", ""), "hook scope for duplicate suppression: user or project")
	baseURL := configStringFlag(fs, "base-url", "OpenAI-compatible base URL (defaults to OPENPE_BASE_URL)")
	apiKey := configStringFlag(fs, "api-key", "OpenAI-compatible API key (defaults to OPENPE_API_KEY)")
	model := configStringFlag(fs, "model", "OpenAI-compatible model (defaults to OPENPE_MODEL)")
	timeout := fs.Duration("timeout", cfg.Timeout, "provider timeout")
	if ok, code := parseFlagSet(fs, args); !ok {
		return code
	}
	input, err := codexadapter.DecodeHookInput(stdin)
	if err != nil {
		return codexadapter.EncodeHookOutputOrFallback(stdout, codexadapter.Skip(localizedInvalidCodexHookInput(err, cfg.Language)))
	}
	manual, shouldHandle := codexadapter.ShouldHandleHook(input, *auto)
	if !shouldHandle {
		return 0
	}
	overrideCWD := ""
	if strings.TrimSpace(input.CWD) == "" {
		workingDir, err := getwd()
		if err != nil {
			return codexadapter.EncodeHookOutputOrFallback(stdout, codexadapter.HookError(manual, fmt.Sprintf("get cwd: %v", err), cfg.Language))
		}
		overrideCWD = workingDir
	}
	effectiveCWD := strings.TrimSpace(input.CWD)
	if overrideCWD != "" {
		effectiveCWD = overrideCWD
	}
	if codexadapter.ShouldSkipDuplicateHook(*hookScope, os.Getenv("OPENPE_ENV_FILE"), effectiveCWD) {
		return 0
	}
	history := codexSessionHistory(input.Prompt, effectiveCWD, cfg)
	provider, err := newProvider(openai.Config{
		BaseURL: baseURL.ValueOrDefault(cfg.BaseURL),
		APIKey:  apiKey.ValueOrDefault(cfg.APIKey),
		Model:   model.ValueOrDefault(cfg.Model),
		Timeout: *timeout,
	})
	if err != nil {
		return codexadapter.EncodeHookOutputOrFallback(stdout, codexadapter.HookError(manual, fmt.Sprintf("configure provider: %v", err), cfg.Language))
	}
	service, err := newEnhancerService(provider, cfg)
	if err != nil {
		return codexadapter.EncodeHookOutputOrFallback(stdout, codexadapter.HookError(manual, fmt.Sprintf("configure context provider: %v", err), cfg.Language))
	}
	output, err := codexadapter.HandleHook(context.Background(), service, input, codexadapter.HookOptions{
		Client:   *client,
		Mode:     *mode,
		Auto:     *auto,
		CWD:      overrideCWD,
		Language: cfg.Language,
		Timeout:  timeoutOrDefault(*timeout),
		History:  history,
	})
	if err != nil {
		return codexadapter.EncodeHookOutputOrFallback(stdout, codexadapter.HookError(manual, err.Error(), cfg.Language))
	}
	if output.Decision == "block" && *copyPreview && output.PreviewPrompt != "" {
		result := delivery.Deliver(context.Background(), output.PreviewPrompt, delivery.Options{
			Client:   "codex",
			Language: cfg.Language,
		})
		output.Reason = delivery.HookStatus(result, cfg.Language, "openpe codex hook last --prompt")
	}
	if output.Decision == "block" && *blockOutput == "stderr" {
		if *terminalPreview {
			_ = codexadapter.WriteTerminalPreview(output.TerminalPreview)
		}
		fmt.Fprintln(stderr, output.Reason)
		return 2
	}
	if output.Decision == "block" && *blockOutput != "json" {
		return codexadapter.EncodeHookOutputOrFallback(stdout, codexadapter.HookError(true, fmt.Sprintf("unsupported block-output %q", *blockOutput), cfg.Language))
	}
	return codexadapter.EncodeHookOutputOrFallback(stdout, output)
}

func codexSessionHistory(prompt string, cwd string, cfg config.Config) []enhancer.Message {
	if !cfg.Codex.History.Enabled {
		return nil
	}
	result, err := codexhistory.New(codexhistory.Options{
		Home:        cfg.Codex.History.Home,
		MaxMessages: cfg.Codex.History.MaxMessages,
		MaxChars:    cfg.Codex.History.MaxChars,
	}).Retrieve(prompt, cwd)
	if err != nil {
		return nil
	}
	return result.Messages
}

func runCodexHookInstall(args []string, stdout io.Writer, stderr io.Writer, getwd func() (string, error)) int {
	fs := flag.NewFlagSet("codex hook install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scope := fs.String("scope", "user", "hook scope: user or project")
	target := fs.String("path", "", "explicit hooks.json path")
	openpeBin := fs.String("openpe-bin", "", "openpe executable path; defaults to PATH lookup")
	envFile := fs.String("env-file", "", "dotenv file loaded by the hook; defaults to project .env")
	hookTimeout := fs.Int("hook-timeout", 120, "Codex hook timeout in seconds")
	dryRun := fs.Bool("dry-run", false, "print hooks.json without writing it")
	if ok, code := parseFlagSet(fs, args); !ok {
		return code
	}
	if *hookTimeout <= 0 {
		fmt.Fprintln(stderr, "hook-timeout must be positive")
		return 1
	}
	hooksPath, err := codexHooksPath(*scope, *target, getwd)
	if err != nil {
		fmt.Fprintf(stderr, "resolve hooks path: %v\n", err)
		return 1
	}
	bin, err := resolveOpenPEBin(*openpeBin)
	if err != nil {
		fmt.Fprintf(stderr, "resolve openpe binary: %v\n", err)
		return 1
	}
	hookEnvFile, err := codexHookEnvFile(*scope, *target, *envFile, getwd)
	if err != nil {
		fmt.Fprintf(stderr, "resolve hook env file: %v\n", err)
		return 1
	}
	var existing []byte
	if data, err := os.ReadFile(hooksPath); err == nil {
		existing = data
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(stderr, "read hooks config: %v\n", err)
		return 1
	}
	command := codexadapter.HookCommandForScope(bin, hookEnvFile, *scope)
	merged, err := codexadapter.MergeHooksConfig(existing, command, *hookTimeout)
	if err != nil {
		fmt.Fprintf(stderr, "merge hooks config: %v\n", err)
		return 1
	}
	changed := !bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace(merged))
	if *dryRun {
		_, _ = stdout.Write(merged)
		return 0
	}
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		fmt.Fprintf(stderr, "create hooks directory: %v\n", err)
		return 1
	}
	if err := os.WriteFile(hooksPath, merged, 0o644); err != nil {
		fmt.Fprintf(stderr, "write hooks config: %v\n", err)
		return 1
	}
	if changed {
		fmt.Fprintf(stdout, "installed openPE Codex hook: %s\n", hooksPath)
	} else {
		fmt.Fprintf(stdout, "openPE Codex hook already installed: %s\n", hooksPath)
	}
	return 0
}

func runClaude(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error)) int {
	if len(args) > 0 && args[0] == "hook" {
		return runClaudeHook(args[1:], stdin, stdout, stderr, newProvider, getwd)
	}
	fmt.Fprintf(stderr, "unknown claude command\n")
	printClaudeHookUsage(stderr)
	return 2
}

func runClaudeHook(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error)) int {
	if len(args) == 0 {
		printClaudeHookUsage(stderr)
		return 2
	}
	switch args[0] {
	case "run":
		return runClaudeHookRun(args[1:], stdin, stderr, newProvider, getwd)
	case "install":
		return runClaudeHookInstall(args[1:], stdout, stderr, getwd)
	case "last":
		return runClaudeHookLast(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		printClaudeHookUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown claude hook command: %s\n", args[0])
		printClaudeHookUsage(stderr)
		return 2
	}
}

func runClaudeHookLast(args []string, stdout io.Writer, stderr io.Writer) int {
	return runDeliveryLast("claude hook last", "claude", args, stdout, stderr)
}

func runClaudeHookRun(args []string, stdin io.Reader, stderr io.Writer, newProvider providerFactory, getwd func() (string, error)) int {
	cfg := config.Load()
	fs := flag.NewFlagSet("claude hook run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	client := fs.String("client", "claude-code", "target client name")
	mode := fs.String("mode", "agent", "prompt mode")
	baseURL := configStringFlag(fs, "base-url", "OpenAI-compatible base URL (defaults to OPENPE_BASE_URL)")
	apiKey := configStringFlag(fs, "api-key", "OpenAI-compatible API key (defaults to OPENPE_API_KEY)")
	model := configStringFlag(fs, "model", "OpenAI-compatible model (defaults to OPENPE_MODEL)")
	timeout := fs.Duration("timeout", cfg.Timeout, "provider timeout")
	if ok, code := parseFlagSet(fs, args); !ok {
		return code
	}
	input, err := claudeadapter.DecodeHookInput(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", localizedInvalidClaudeHookInput(err, cfg.Language))
		return 0
	}
	if !claudeadapter.ShouldHandleHook(input) {
		return 0
	}
	overrideCWD := ""
	effectiveCWD := strings.TrimSpace(input.CWD)
	if effectiveCWD == "" {
		workingDir, err := getwd()
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", localizedEnhanceFailure(fmt.Sprintf("get cwd: %v", err), cfg.Language))
			return 2
		}
		overrideCWD = workingDir
		effectiveCWD = workingDir
	}
	history := claudeTranscriptHistory(input.TranscriptPath, effectiveCWD, cfg)
	provider, err := newProvider(openai.Config{
		BaseURL: baseURL.ValueOrDefault(cfg.BaseURL),
		APIKey:  apiKey.ValueOrDefault(cfg.APIKey),
		Model:   model.ValueOrDefault(cfg.Model),
		Timeout: *timeout,
	})
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", localizedEnhanceFailure(fmt.Sprintf("configure provider: %v", err), cfg.Language))
		return 2
	}
	service, err := newEnhancerService(provider, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", localizedEnhanceFailure(fmt.Sprintf("configure context provider: %v", err), cfg.Language))
		return 2
	}
	output, err := claudeadapter.HandleHook(context.Background(), service, input, claudeadapter.HookOptions{
		Client:   *client,
		Mode:     *mode,
		CWD:      overrideCWD,
		Language: cfg.Language,
		Timeout:  timeoutOrDefault(*timeout),
		History:  history,
	})
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", localizedEnhanceFailure(err.Error(), cfg.Language))
		return 2
	}
	if strings.TrimSpace(output.PreviewPrompt) == "" {
		return 0
	}
	result := delivery.Deliver(context.Background(), output.PreviewPrompt, delivery.Options{
		Client:   "claude",
		Language: cfg.Language,
	})
	fmt.Fprintln(stderr, delivery.HookStatus(result, cfg.Language, "openpe claude hook last --prompt"))
	return 2
}

func claudeTranscriptHistory(transcriptPath string, cwd string, cfg config.Config) []enhancer.Message {
	if !cfg.Claude.Transcript.Enabled {
		return nil
	}
	result, err := claudetranscript.New(claudetranscript.Options{
		MaxMessages: cfg.Claude.Transcript.MaxMessages,
		MaxChars:    cfg.Claude.Transcript.MaxChars,
	}).Retrieve(transcriptPath, cwd)
	if err != nil {
		return nil
	}
	return result.Messages
}

func runClaudeHookInstall(args []string, stdout io.Writer, stderr io.Writer, getwd func() (string, error)) int {
	fs := flag.NewFlagSet("claude hook install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	target := fs.String("path", "", "explicit Claude settings.json path")
	openpeBin := fs.String("openpe-bin", "", "openpe executable path; defaults to PATH lookup")
	envFile := fs.String("env-file", "", "dotenv file loaded by the hook; defaults to ~/.config/openpe/.env")
	hookTimeout := fs.Int("hook-timeout", 120, "Claude hook timeout in seconds")
	dryRun := fs.Bool("dry-run", false, "print settings.json without writing it")
	if ok, code := parseFlagSet(fs, args); !ok {
		return code
	}
	if *hookTimeout <= 0 {
		fmt.Fprintln(stderr, "hook-timeout must be positive")
		return 1
	}
	settingsPath, err := claudeSettingsPath(*target)
	if err != nil {
		fmt.Fprintf(stderr, "resolve Claude settings path: %v\n", err)
		return 1
	}
	bin, err := resolveOpenPEBin(*openpeBin)
	if err != nil {
		fmt.Fprintf(stderr, "resolve openpe binary: %v\n", err)
		return 1
	}
	hookEnvFile, err := claudeHookEnvFile(*envFile)
	if err != nil {
		fmt.Fprintf(stderr, "resolve hook env file: %v\n", err)
		return 1
	}
	var existing []byte
	if data, err := os.ReadFile(settingsPath); err == nil {
		existing = data
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(stderr, "read Claude settings: %v\n", err)
		return 1
	}
	command := claudeadapter.HookCommand(bin, hookEnvFile)
	merged, err := claudeadapter.MergeSettings(existing, command, *hookTimeout)
	if err != nil {
		fmt.Fprintf(stderr, "merge Claude settings: %v\n", err)
		return 1
	}
	changed := !bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace(merged))
	if *dryRun {
		_, _ = stdout.Write(merged)
		return 0
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		fmt.Fprintf(stderr, "create Claude settings directory: %v\n", err)
		return 1
	}
	if err := os.WriteFile(settingsPath, merged, 0o644); err != nil {
		fmt.Fprintf(stderr, "write Claude settings: %v\n", err)
		return 1
	}
	if changed {
		fmt.Fprintf(stdout, "installed openPE Claude hook: %s\n", settingsPath)
	} else {
		fmt.Fprintf(stdout, "openPE Claude hook already installed: %s\n", settingsPath)
	}
	return 0
}

func runWindsurf(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error)) int {
	if len(args) > 0 && args[0] == "hook" {
		return runWindsurfHook(args[1:], stdin, stdout, stderr, newProvider, getwd)
	}
	fmt.Fprintf(stderr, "unknown windsurf command\n")
	printWindsurfHookUsage(stderr)
	return 2
}

func runWindsurfHook(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error)) int {
	if len(args) == 0 {
		printWindsurfHookUsage(stderr)
		return 2
	}
	switch args[0] {
	case "run":
		return runWindsurfHookRun(args[1:], stdin, stderr, newProvider, getwd)
	case "install":
		return runWindsurfHookInstall(args[1:], stdout, stderr, getwd)
	case "last":
		return runWindsurfHookLast(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		printWindsurfHookUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown windsurf hook command: %s\n", args[0])
		printWindsurfHookUsage(stderr)
		return 2
	}
}

func runWindsurfHookRun(args []string, stdin io.Reader, stderr io.Writer, newProvider providerFactory, getwd func() (string, error)) int {
	cfg := config.Load()
	fs := flag.NewFlagSet("windsurf hook run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	client := fs.String("client", "windsurf", "target client name")
	mode := fs.String("mode", "cascade", "prompt mode")
	baseURL := configStringFlag(fs, "base-url", "OpenAI-compatible base URL (defaults to OPENPE_BASE_URL)")
	apiKey := configStringFlag(fs, "api-key", "OpenAI-compatible API key (defaults to OPENPE_API_KEY)")
	model := configStringFlag(fs, "model", "OpenAI-compatible model (defaults to OPENPE_MODEL)")
	timeout := fs.Duration("timeout", cfg.Timeout, "provider timeout")
	if ok, code := parseFlagSet(fs, args); !ok {
		return code
	}
	input, err := windsurfadapter.DecodeHookInput(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", localizedInvalidWindsurfHookInput(err, cfg.Language))
		return 0
	}
	if !windsurfadapter.ShouldHandleHook(input) {
		return 0
	}
	overrideCWD := ""
	if strings.TrimSpace(input.CWD) == "" {
		workingDir, err := getwd()
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", localizedEnhanceFailure(fmt.Sprintf("get cwd: %v", err), cfg.Language))
			return 2
		}
		overrideCWD = workingDir
	}
	provider, err := newProvider(openai.Config{
		BaseURL: baseURL.ValueOrDefault(cfg.BaseURL),
		APIKey:  apiKey.ValueOrDefault(cfg.APIKey),
		Model:   model.ValueOrDefault(cfg.Model),
		Timeout: *timeout,
	})
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", localizedEnhanceFailure(fmt.Sprintf("configure provider: %v", err), cfg.Language))
		return 2
	}
	service, err := newEnhancerService(provider, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", localizedEnhanceFailure(fmt.Sprintf("configure context provider: %v", err), cfg.Language))
		return 2
	}
	output, err := windsurfadapter.HandleHook(context.Background(), service, input, windsurfadapter.HookOptions{
		Client:   *client,
		Mode:     *mode,
		CWD:      overrideCWD,
		Language: cfg.Language,
		Timeout:  timeoutOrDefault(*timeout),
	})
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", localizedEnhanceFailure(err.Error(), cfg.Language))
		return 2
	}
	if strings.TrimSpace(output.PreviewPrompt) == "" {
		return 0
	}
	result := delivery.Deliver(context.Background(), output.PreviewPrompt, delivery.Options{
		Client:   "windsurf",
		Language: cfg.Language,
	})
	fmt.Fprintln(stderr, delivery.HookStatus(result, cfg.Language, "openpe windsurf hook last --prompt"))
	return 2
}

func runWindsurfHookLast(args []string, stdout io.Writer, stderr io.Writer) int {
	return runDeliveryLast("windsurf hook last", "windsurf", args, stdout, stderr)
}

func runWindsurfHookInstall(args []string, stdout io.Writer, stderr io.Writer, getwd func() (string, error)) int {
	fs := flag.NewFlagSet("windsurf hook install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scope := fs.String("scope", "user", "hook scope: user or project")
	target := fs.String("path", "", "explicit Windsurf hooks.json path")
	openpeBin := fs.String("openpe-bin", "", "openpe executable path; defaults to PATH lookup")
	envFile := fs.String("env-file", "", "dotenv file loaded by the hook; defaults to ~/.config/openpe/.env for user hooks or project .env for project hooks")
	dryRun := fs.Bool("dry-run", false, "print hooks.json without writing it")
	if ok, code := parseFlagSet(fs, args); !ok {
		return code
	}
	hooksPath, err := windsurfHooksPath(*scope, *target, getwd)
	if err != nil {
		fmt.Fprintf(stderr, "resolve Windsurf hooks path: %v\n", err)
		return 1
	}
	bin, err := resolveOpenPEBin(*openpeBin)
	if err != nil {
		fmt.Fprintf(stderr, "resolve openpe binary: %v\n", err)
		return 1
	}
	hookEnvFile, err := windsurfHookEnvFile(*scope, *target, *envFile, getwd)
	if err != nil {
		fmt.Fprintf(stderr, "resolve hook env file: %v\n", err)
		return 1
	}
	var existing []byte
	if data, err := os.ReadFile(hooksPath); err == nil {
		existing = data
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(stderr, "read Windsurf hooks config: %v\n", err)
		return 1
	}
	command := windsurfadapter.HookCommand(bin, hookEnvFile)
	powershell := windsurfadapter.PowerShellHookCommand(bin, hookEnvFile)
	merged, err := windsurfadapter.MergeHooksConfig(existing, command, powershell)
	if err != nil {
		fmt.Fprintf(stderr, "merge Windsurf hooks config: %v\n", err)
		return 1
	}
	changed := !bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace(merged))
	if *dryRun {
		_, _ = stdout.Write(merged)
		return 0
	}
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		fmt.Fprintf(stderr, "create Windsurf hooks directory: %v\n", err)
		return 1
	}
	if err := os.WriteFile(hooksPath, merged, 0o644); err != nil {
		fmt.Fprintf(stderr, "write Windsurf hooks config: %v\n", err)
		return 1
	}
	if changed {
		fmt.Fprintf(stdout, "installed openPE Windsurf hook: %s\n", hooksPath)
	} else {
		fmt.Fprintf(stdout, "openPE Windsurf hook already installed: %s\n", hooksPath)
	}
	return 0
}

func codexHooksPath(scope string, target string, getwd func() (string, error)) (string, error) {
	if strings.TrimSpace(target) != "" {
		return filepath.Clean(target), nil
	}
	switch scope {
	case "project":
		cwd, err := getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".codex", "hooks.json"), nil
	case "user":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".codex", "hooks.json"), nil
	default:
		return "", fmt.Errorf("unsupported scope %q", scope)
	}
}

func claudeSettingsPath(target string) (string, error) {
	if strings.TrimSpace(target) != "" {
		return filepath.Abs(target)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

func windsurfHooksPath(scope string, target string, getwd func() (string, error)) (string, error) {
	if strings.TrimSpace(target) != "" {
		return filepath.Clean(target), nil
	}
	switch scope {
	case "project":
		cwd, err := getwd()
		if err != nil {
			return "", err
		}
		return windsurfadapter.ProjectHooksPath(cwd), nil
	case "user":
		path := windsurfadapter.UserHooksPath()
		if path == "" {
			return "", fmt.Errorf("resolve user home directory")
		}
		return path, nil
	default:
		return "", fmt.Errorf("unsupported scope %q", scope)
	}
}

func resolveOpenPEBin(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value != "" {
		if strings.ContainsAny(value, `/\`) {
			return filepath.Abs(value)
		}
		if found, err := exec.LookPath(value); err == nil {
			return filepath.Abs(found)
		}
		return filepath.Abs(value)
	}
	if found, err := exec.LookPath("openpe"); err == nil {
		return filepath.Abs(found)
	}
	return os.Executable()
}

func codexHookEnvFile(scope string, target string, value string, getwd func() (string, error)) (string, error) {
	value = strings.TrimSpace(value)
	if value != "" {
		return filepath.Abs(value)
	}
	if scope == "user" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config", "openpe", ".env"), nil
	}
	if strings.TrimSpace(target) != "" {
		return "", nil
	}
	cwd, err := getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, ".env"), nil
}

func claudeHookEnvFile(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value != "" {
		return filepath.Abs(value)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "openpe", ".env"), nil
}

func windsurfHookEnvFile(scope string, target string, value string, getwd func() (string, error)) (string, error) {
	value = strings.TrimSpace(value)
	if value != "" {
		return filepath.Abs(value)
	}
	if scope == "user" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config", "openpe", ".env"), nil
	}
	if strings.TrimSpace(target) != "" {
		return "", nil
	}
	cwd, err := getwd()
	if err != nil {
		return "", err
	}
	return windsurfadapter.ProjectEnvFile(cwd), nil
}

func runCommand(ctx context.Context, name string, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func newEnhancerService(provider enhancer.Provider, cfg config.Config) (*enhancer.Service, error) {
	if !cfg.Openace.Enabled {
		return enhancer.NewService(provider), nil
	}
	contextProvider, err := openacectx.New(openacectx.Config{
		DaemonAddr:        cfg.Openace.Addr,
		DaemonToken:       cfg.Openace.Token,
		ProviderProfileID: cfg.Openace.ProviderProfileID,
		MaxOutputLength:   cfg.Openace.MaxOutputLength,
		Timeout:           cfg.Openace.Timeout,
		MaxRetries:        cfg.Openace.MaxRetries,
		RetryBaseDelay:    cfg.Openace.RetryBaseDelay,
		RetryMaxDelay:     cfg.Openace.RetryMaxDelay,
		RetryJitter:       cfg.Openace.RetryJitter,
	})
	if err != nil {
		return nil, err
	}
	return enhancer.NewServiceWithContext(provider, contextProvider), nil
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
	fmt.Fprintln(w, "正式使用：安装 hook 后，在 Codex、Claude Code 或 Windsurf Cascade 对话终端输入 `pe <内容>`。")
	fmt.Fprintln(w, "测试/调试命令：")
	fmt.Fprintln(w, "  openpe codex hook install [--scope project|user]")
	fmt.Fprintln(w, "  openpe claude hook install")
	fmt.Fprintln(w, "  openpe windsurf hook install [--scope project|user]")
	fmt.Fprintln(w, "  openpe enhance [--prompt text] [--json] [--client name] [--mode name]")
	fmt.Fprintln(w, "  openpe codex [--prompt text] [--dry-run] [--codex-arg arg]...")
	fmt.Fprintln(w, "  openpe codex hook run")
	fmt.Fprintln(w, "  openpe codex last [--path] [--prompt]")
	fmt.Fprintln(w, "  openpe claude hook run")
	fmt.Fprintln(w, "  openpe claude hook last [--path] [--prompt]")
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
