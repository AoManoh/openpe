package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	claudeadapter "github.com/AoManoh/openpe/internal/adapters/claude"
	"github.com/AoManoh/openpe/internal/adapters/clipboard"
	codexadapter "github.com/AoManoh/openpe/internal/adapters/codex"
	"github.com/AoManoh/openpe/internal/adapters/delivery"
	devinadapter "github.com/AoManoh/openpe/internal/adapters/devin"
	"github.com/AoManoh/openpe/internal/adapters/hookdedup"
	windsurfadapter "github.com/AoManoh/openpe/internal/adapters/windsurf"
	"github.com/AoManoh/openpe/internal/config"
	claudetranscript "github.com/AoManoh/openpe/internal/context/claudetranscript"
	codexhistory "github.com/AoManoh/openpe/internal/context/codexhistory"
	devinhistory "github.com/AoManoh/openpe/internal/context/devinhistory"
	devinsession "github.com/AoManoh/openpe/internal/context/devinsession"
	"github.com/AoManoh/openpe/internal/context/histstatus"
	openacectx "github.com/AoManoh/openpe/internal/context/openace"
	"github.com/AoManoh/openpe/internal/enhancer"
	"github.com/AoManoh/openpe/internal/providers"
)

// Version is the build identifier exposed via `openpe --version`. The
// default "dev" matches `go install ./cmd/openpe` users who do not pass
// ldflags; release builds should override it with the git tag / commit:
//
//	go build -ldflags "-X main.Version=v0.2.0" ./cmd/openpe
var Version = "dev"

type providerFactory func(providers.Spec) (enhancer.Provider, error)
type commandRunner func(ctx context.Context, name string, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error

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
	cfg := config.Load()
	opts := configuredDeliveryOptions(cfg, client)
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
		if opts.CacheDir != "" {
			path, err = delivery.LastPreviewPathWithOptions(client, opts)
			if *promptOnly {
				path, err = delivery.LastPromptPathWithOptions(client, opts)
			}
		}
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
	if opts.CacheDir != "" {
		content, err = delivery.ReadLastPreviewWithOptions(client, opts)
		if *promptOnly {
			content, err = delivery.ReadLastPromptWithOptions(client, opts)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "read cached content: %v\n", err)
		return 1
	}
	fmt.Fprint(stdout, content)
	return 0
}

func configuredDeliveryOptions(cfg config.Config, client string) delivery.Options {
	clipboardOpts := clipboard.Options{
		Command:      cfg.Delivery.CopyCommand,
		DisableOSC52: cfg.Delivery.DisableOSC52Clipboard,
		OSC52TTY:     cfg.Delivery.OSC52TTY,
	}
	return delivery.Options{
		Client:    client,
		Language:  cfg.Language,
		CacheDir:  cfg.Delivery.CacheDir,
		Clipboard: &clipboardOpts,
	}
}

func hookLastPromptCommand(client string) string {
	command := "openpe " + client + " hook last --prompt"
	if envFile := strings.TrimSpace(os.Getenv("OPENPE_ENV_FILE")); envFile != "" {
		return "OPENPE_ENV_FILE=" + shellQuoteStatus(envFile) + " " + command
	}
	return command
}

func shellQuoteStatus(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
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
	history, histStatus, histErr := codexSessionHistory(input.Prompt, effectiveCWD, cfg)
	provider, err := newProvider(providers.Spec{
		Provider:  cfg.Provider,
		MaxTokens: cfg.MaxTokens,
		BaseURL:   baseURL.ValueOrDefault(cfg.BaseURL),
		APIKey:    apiKey.ValueOrDefault(cfg.APIKey),
		Model:     model.ValueOrDefault(cfg.Model),
		Timeout:   *timeout,
	})
	if err != nil {
		return codexadapter.EncodeHookOutputOrFallback(stdout, codexadapter.HookError(manual, fmt.Sprintf("configure provider: %v", err), cfg.Language))
	}
	service, err := newEnhancerService(provider, cfg)
	if err != nil {
		return codexadapter.EncodeHookOutputOrFallback(stdout, codexadapter.HookError(manual, fmt.Sprintf("configure context provider: %v", err), cfg.Language))
	}
	output, err := codexadapter.HandleHook(context.Background(), service, input, codexadapter.HookOptions{
		Client:           *client,
		Mode:             *mode,
		Auto:             *auto,
		CWD:              overrideCWD,
		Language:         cfg.Language,
		Timeout:          timeoutOrDefault(*timeout),
		History:          history,
		Inject:           cfg.Inject.Codex,
		MaxContextTokens: cfg.MaxContextTokens,
	})
	if err != nil {
		return codexadapter.EncodeHookOutputOrFallback(stdout, codexadapter.HookError(manual, err.Error(), cfg.Language))
	}
	if output.Decision == "block" && *copyPreview && output.PreviewPrompt != "" {
		result := delivery.Deliver(context.Background(), output.PreviewPrompt, configuredDeliveryOptions(cfg, "codex"))
		output.Reason = delivery.HookStatus(result, cfg.Language, hookLastPromptCommand("codex"))
	}
	// Non-silent disclosure: always state whether prior context was included
	// (and if not, why / or that reading failed), so a history-less result is
	// never mistaken for a context-aware one.
	if note := historyDisclosure(history, histStatus, 0, histErr, cfg.Language); note != "" {
		if output.Decision == "block" {
			output.Reason = strings.TrimSpace(note + " " + output.Reason)
		} else {
			output.SystemMessage = strings.TrimSpace(note + " " + output.SystemMessage)
		}
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

// codexSessionHistory returns the Codex session history to inject. The error is
// non-nil only on a genuine read failure (not a quiet "no history available"
// skip); callers surface it so the user is not misled into thinking the
// enhancement included history when reading it actually failed.
func codexSessionHistory(prompt string, cwd string, cfg config.Config) ([]enhancer.Message, histstatus.Status, error) {
	if !cfg.Codex.History.Enabled {
		return nil, histstatus.Unknown, nil
	}
	result, err := codexhistory.New(codexhistory.Options{
		Home:        cfg.Codex.History.Home,
		MaxMessages: cfg.Codex.History.MaxMessages,
		MaxChars:    cfg.Codex.History.MaxChars,
	}).Retrieve(prompt, cwd)
	return result.Messages, result.Status, err
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
		return runClaudeHookRun(args[1:], stdin, stdout, stderr, newProvider, getwd)
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

func runClaudeHookRun(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error)) int {
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
	history, histStatus, histErr := claudeTranscriptHistory(input.TranscriptPath, effectiveCWD, cfg)
	provider, err := newProvider(providers.Spec{
		Provider:  cfg.Provider,
		MaxTokens: cfg.MaxTokens,
		BaseURL:   baseURL.ValueOrDefault(cfg.BaseURL),
		APIKey:    apiKey.ValueOrDefault(cfg.APIKey),
		Model:     model.ValueOrDefault(cfg.Model),
		Timeout:   *timeout,
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
	// When the Devin CLI imports and runs this Claude hook, render Devin-native
	// JSON output (de-duplicated against sibling openPE hooks) instead of the
	// Claude exit-2 + clipboard delivery, which Devin misreads as an empty block.
	if runningUnderDevin() {
		return renderDevinHook(cfg, service, input.Prompt, effectiveCWD, stdout)
	}
	output, err := claudeadapter.HandleHook(context.Background(), service, input, claudeadapter.HookOptions{
		Client:           *client,
		Mode:             *mode,
		CWD:              overrideCWD,
		Language:         cfg.Language,
		Timeout:          timeoutOrDefault(*timeout),
		History:          history,
		Inject:           cfg.Inject.Claude,
		MaxContextTokens: cfg.MaxContextTokens,
	})
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", localizedEnhanceFailure(err.Error(), cfg.Language))
		return 2
	}
	// Inject mode (OPENPE_HOOK_INJECT / OPENPE_CLAUDE_INJECT): emit exit-0 JSON
	// with additionalContext (Claude CLI injects it via a system-reminder) plus
	// the non-silent history disclosure; cache the prompt for `claude hook last`.
	if output.Injected {
		_, _ = delivery.Save("claude", output.PreviewPrompt, cfg.Language)
		if note := historyDisclosure(history, histStatus, 0, histErr, cfg.Language); note != "" {
			output.SystemMessage = strings.TrimSpace(note + " " + output.SystemMessage)
		}
		_ = claudeadapter.EncodeInjection(stdout, output)
		return 0
	}
	if strings.TrimSpace(output.PreviewPrompt) == "" {
		return 0
	}
	result := delivery.Deliver(context.Background(), output.PreviewPrompt, configuredDeliveryOptions(cfg, "claude"))
	status := delivery.HookStatus(result, cfg.Language, hookLastPromptCommand("claude"))
	if result.CopyError != nil && cfg.Delivery.ClaudePromptFallback {
		status = delivery.AppendPromptFallback(status, output.PreviewPrompt, cfg.Language)
	}
	// Non-silent disclosure: always state whether prior context was included
	// (and if not, why / or that reading failed).
	if note := historyDisclosure(history, histStatus, 0, histErr, cfg.Language); note != "" {
		status = note + "\n" + status
	}
	fmt.Fprintln(stderr, status)
	return 2
}

// claudeTranscriptHistory returns the Claude transcript history to inject. The
// error is non-nil only on a genuine read failure (not a quiet "no transcript"
// skip); the caller surfaces it so a failed read is not mistaken for a
// history-aware enhancement.
func claudeTranscriptHistory(transcriptPath string, cwd string, cfg config.Config) ([]enhancer.Message, histstatus.Status, error) {
	if !cfg.Claude.Transcript.Enabled {
		return nil, histstatus.Unknown, nil
	}
	result, err := claudetranscript.New(claudetranscript.Options{
		MaxMessages: cfg.Claude.Transcript.MaxMessages,
		MaxChars:    cfg.Claude.Transcript.MaxChars,
	}).Retrieve(transcriptPath, cwd)
	return result.Messages, result.Status, err
}

// devinSessionHistory returns the current Devin session history to inject when
// running under the Devin CLI. Like the codex/claude helpers, the error is
// non-nil only on a genuine read failure; "no session", "stale", "empty" and
// "ambiguous" are reported via the status so the hook layer surfaces them
// explicitly. On Linux the session is identified exactly from the hook's
// process ancestry (the devin ancestor holds session_locks/<id>.lock open),
// which makes same-directory parallel sessions safe; elsewhere — or if
// discovery fails — the collector falls back to the cwd+recency heuristic,
// which refuses to guess between multiple live sessions (Ambiguous).
func devinSessionHistory(prompt string, cwd string, cfg config.Config) (devinhistory.Result, error) {
	if !cfg.Devin.History.Enabled {
		return devinhistory.Result{Status: histstatus.Unknown}, nil
	}
	return devinhistory.New(devinhistory.Options{
		DBPath:      cfg.Devin.History.DBPath,
		MaxMessages: cfg.Devin.History.MaxMessages,
		MaxChars:    cfg.Devin.History.MaxChars,
		Recency:     cfg.Devin.History.Recency,
		SessionID:   discoverDevinSessionID(cfg.Devin.History.DBPath),
	}).Retrieve(prompt, cwd)
}

// discoverDevinSessionID resolves the identity of the Devin session this hook
// process belongs to (Linux /proc ancestry walk; "" elsewhere or on failure).
func discoverDevinSessionID(dbPath string) string {
	if runtime.GOOS != "linux" {
		return ""
	}
	if strings.TrimSpace(dbPath) == "" {
		dbPath = devinhistory.DefaultDBPath()
	}
	return devinsession.Discover("/proc", devinsession.DefaultLockDir(dbPath), os.Getpid())
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
		return runWindsurfHookRun(args[1:], stdin, stdout, stderr, newProvider, getwd)
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

func runWindsurfHookRun(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error)) int {
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
	provider, err := newProvider(providers.Spec{
		Provider:  cfg.Provider,
		MaxTokens: cfg.MaxTokens,
		BaseURL:   baseURL.ValueOrDefault(cfg.BaseURL),
		APIKey:    apiKey.ValueOrDefault(cfg.APIKey),
		Model:     model.ValueOrDefault(cfg.Model),
		Timeout:   *timeout,
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
	// When the Devin CLI imports and runs this Windsurf hook, render Devin-native
	// JSON output (de-duplicated against sibling openPE hooks) instead of the
	// Windsurf exit-2 + clipboard delivery, which Devin misreads as an empty block.
	if runningUnderDevin() {
		return renderDevinHook(cfg, service, input.ToolInfo.UserPrompt, input.CWD, stdout)
	}
	output, err := windsurfadapter.HandleHook(context.Background(), service, input, windsurfadapter.HookOptions{
		Client:           *client,
		Mode:             *mode,
		CWD:              overrideCWD,
		Language:         cfg.Language,
		Timeout:          timeoutOrDefault(*timeout),
		MaxContextTokens: cfg.MaxContextTokens,
	})
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", localizedEnhanceFailure(err.Error(), cfg.Language))
		return 2
	}
	if strings.TrimSpace(output.PreviewPrompt) == "" {
		return 0
	}
	result := delivery.Deliver(context.Background(), output.PreviewPrompt, configuredDeliveryOptions(cfg, "windsurf"))
	status := delivery.HookStatus(result, cfg.Language, hookLastPromptCommand("windsurf"))
	if result.CopyError != nil && cfg.Delivery.WindsurfPromptFallback {
		status = delivery.AppendPromptFallback(status, output.PreviewPrompt, cfg.Language)
	}
	fmt.Fprintln(stderr, status)
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

func runDevin(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error)) int {
	if len(args) > 0 && args[0] == "hook" {
		return runDevinHook(args[1:], stdin, stdout, stderr, newProvider, getwd)
	}
	fmt.Fprintf(stderr, "unknown devin command\n")
	printDevinHookUsage(stderr)
	return 2
}

func runDevinHook(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error)) int {
	if len(args) == 0 {
		printDevinHookUsage(stderr)
		return 2
	}
	switch args[0] {
	case "run":
		return runDevinHookRun(args[1:], stdin, stdout, stderr, newProvider, getwd)
	case "install":
		return runDevinHookInstall(args[1:], stdout, stderr, getwd)
	case "last":
		return runDevinHookLast(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		printDevinHookUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown devin hook command: %s\n", args[0])
		printDevinHookUsage(stderr)
		return 2
	}
}

func runDevinHookLast(args []string, stdout io.Writer, stderr io.Writer) int {
	return runDeliveryLast("devin hook last", "devin", args, stdout, stderr)
}

func runDevinHookRun(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error)) int {
	cfg := config.Load()
	fs := flag.NewFlagSet("devin hook run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	client := fs.String("client", "devin", "target client name")
	mode := fs.String("mode", "agent", "prompt mode")
	auto := fs.Bool("auto", false, "enhance every prompt and inject it as additional context")
	blockOutput := fs.String("block-output", "json", "preview block output: json or stderr")
	terminalPreview := fs.Bool("terminal-preview", envBoolOrDefault("OPENPE_DEVIN_TERMINAL_PREVIEW", false), "experimental: write full Markdown preview directly to /dev/tty when blocking")
	copyPreview := fs.Bool("copy-preview", envBoolOrDefault("OPENPE_DEVIN_COPY_PREVIEW", true), "copy enhanced prompt to the system clipboard when blocking")
	hookScope := fs.String("hook-scope", envOrDefault("OPENPE_HOOK_SCOPE", ""), "hook scope for duplicate suppression: user or project")
	baseURL := configStringFlag(fs, "base-url", "OpenAI-compatible base URL (defaults to OPENPE_BASE_URL)")
	apiKey := configStringFlag(fs, "api-key", "OpenAI-compatible API key (defaults to OPENPE_API_KEY)")
	model := configStringFlag(fs, "model", "OpenAI-compatible model (defaults to OPENPE_MODEL)")
	timeout := fs.Duration("timeout", cfg.Timeout, "provider timeout")
	if ok, code := parseFlagSet(fs, args); !ok {
		return code
	}
	input, err := devinadapter.DecodeHookInput(stdin)
	if err != nil {
		return devinadapter.EncodeHookOutputOrFallback(stdout, devinadapter.Skip(localizedInvalidDevinHookInput(err, cfg.Language)))
	}
	manualTrigger, shouldHandle := devinadapter.ShouldHandleHook(input, *auto)
	if !shouldHandle {
		return 0
	}
	// A project-scope hook is redundant when a user-scope openPE hook already
	// exists, so suppress it to avoid enhancing the same prompt twice.
	if strings.TrimSpace(*hookScope) == "project" && devinadapter.HasUserOpenPEHookConfig() {
		return 0
	}
	// Cross-adapter single-flight: Devin also imports the Claude- and
	// Windsurf-format openPE hooks, so a single prompt can fire several openPE
	// hooks in sequence. The first claims the prompt and enhances it; later
	// firings inside the window mirror the winner's outcome: after a review
	// block they replay the block from the delivery cache (a plain skip here
	// once passed the raw resubmitted `pe` prompt through to the model),
	// after an injection they skip so the prompt is enhanced exactly once.
	claimOutcome := hookdedup.OutcomeUnknown
	if cfg.HookDedup.Enabled {
		won, prior, finish := hookdedup.Claim(cfg.Delivery.CacheDir, input.Prompt, cfg.HookDedup.Window)
		if !won {
			if prior == hookdedup.OutcomeBlock {
				return replayDevinBlockedPrompt(cfg, *copyPreview, *blockOutput, *terminalPreview, stdout, stderr)
			}
			return devinadapter.EncodeHookOutputOrFallback(stdout, devinadapter.SkipOutput())
		}
		defer func() { finish(claimOutcome) }()
	}
	// Devin's UserPromptSubmit stdin does not carry cwd; fall back to
	// DEVIN_PROJECT_DIR (set for command hooks) then the process cwd.
	overrideCWD := strings.TrimSpace(input.CWD)
	if overrideCWD == "" {
		overrideCWD = strings.TrimSpace(os.Getenv("DEVIN_PROJECT_DIR"))
	}
	if overrideCWD == "" {
		workingDir, err := getwd()
		if err != nil {
			return devinadapter.EncodeHookOutputOrFallback(stdout, devinadapter.HookError(manualTrigger, fmt.Sprintf("get cwd: %v", err), cfg.Language))
		}
		overrideCWD = workingDir
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
		return devinadapter.EncodeHookOutputOrFallback(stdout, devinadapter.HookError(manualTrigger, fmt.Sprintf("configure provider: %v", err), cfg.Language))
	}
	service, err := newEnhancerService(provider, cfg)
	if err != nil {
		return devinadapter.EncodeHookOutputOrFallback(stdout, devinadapter.HookError(manualTrigger, fmt.Sprintf("configure context provider: %v", err), cfg.Language))
	}
	hist, histErr := devinSessionHistory(input.Prompt, overrideCWD, cfg)
	output, err := devinadapter.HandleHook(context.Background(), service, input, devinadapter.HookOptions{
		Client:   *client,
		Mode:     *mode,
		Auto:     *auto,
		CWD:      overrideCWD,
		Language: cfg.Language,
		Timeout:  timeoutOrDefault(*timeout),
		History:  hist.Messages,
		// Default false: hold a manual `pe` for review (block + show the enhanced
		// prompt) so the user controls what is sent. The unified inject switch
		// (OPENPE_HOOK_INJECT / OPENPE_DEVIN_INJECT, resolved in config) is the
		// opt-in "I trust it" mode. --auto is inherently inject — it would be
		// unusable if it blocked every prompt.
		Inject:           *auto || cfg.Inject.Devin,
		MaxContextTokens: cfg.MaxContextTokens,
	})
	if err != nil {
		return devinadapter.EncodeHookOutputOrFallback(stdout, devinadapter.HookError(manualTrigger, err.Error(), cfg.Language))
	}
	claimOutcome = dedupOutcomeFor(output)
	if *copyPreview {
		output = deliverDevinBlock(cfg, output)
	}
	output = applyDevinDisclosure(output, hist, histErr, cfg.Language)
	if output.Decision == "block" && *blockOutput == "stderr" {
		if *terminalPreview {
			_ = devinadapter.WriteTerminalPreview(output.TerminalPreview)
		}
		fmt.Fprintln(stderr, output.Reason)
		return 2
	}
	if output.Decision == "block" && *blockOutput != "json" {
		return devinadapter.EncodeHookOutputOrFallback(stdout, devinadapter.HookError(true, fmt.Sprintf("unsupported block-output %q", *blockOutput), cfg.Language))
	}
	return devinadapter.EncodeHookOutputOrFallback(stdout, output)
}

// runningUnderDevin reports whether this hook process was invoked by the Devin
// CLI. Devin sets DEVIN_PROJECT_DIR for every command hook it runs — including
// the Claude- and Windsurf-format hooks it imports — so it is the reliable
// signal that the codex/claude/windsurf adapters must render Devin-native JSON
// output instead of their exit-2 + stderr delivery, which Devin misreads as an
// empty "Prompt blocked:".
func runningUnderDevin() bool {
	return strings.TrimSpace(os.Getenv("DEVIN_PROJECT_DIR")) != ""
}

// renderDevinHook enhances rawPrompt and emits Devin-native JSON output,
// applying cross-adapter single-flight de-duplication. It is shared by the
// claude/windsurf hook runs when the Devin CLI imports and invokes them, so a
// Claude- or Windsurf-installed hook still produces a correct single Devin
// injection. rawPrompt is the unparsed user text (still carrying the `pe`
// trigger); the Devin adapter parses it.
func renderDevinHook(cfg config.Config, service *enhancer.Service, rawPrompt string, cwd string, stdout io.Writer) int {
	claimOutcome := hookdedup.OutcomeUnknown
	if cfg.HookDedup.Enabled {
		won, prior, finish := hookdedup.Claim(cfg.Delivery.CacheDir, rawPrompt, cfg.HookDedup.Window)
		if !won {
			if prior == hookdedup.OutcomeBlock {
				// Same replay as the native devin hook path: a skip here would
				// pass the raw resubmitted `pe` prompt through to the model.
				return replayDevinBlockedPrompt(cfg, true, "json", false, stdout, io.Discard)
			}
			return devinadapter.EncodeHookOutputOrFallback(stdout, devinadapter.SkipOutput())
		}
		defer func() { finish(claimOutcome) }()
	}
	if strings.TrimSpace(cwd) == "" {
		cwd = strings.TrimSpace(os.Getenv("DEVIN_PROJECT_DIR"))
	}
	// Under Devin the context is always the current Devin session (read from
	// its local SQLite store), regardless of which client's hook fired.
	hist, histErr := devinSessionHistory(rawPrompt, cwd, cfg)
	output, err := devinadapter.HandleHook(context.Background(), service, devinadapter.HookInput{
		HookEventName: devinadapter.UserPromptSubmit,
		Prompt:        rawPrompt,
		CWD:           cwd,
	}, devinadapter.HookOptions{
		Client:   "devin",
		Mode:     "agent",
		Language: cfg.Language,
		Timeout:  timeoutOrDefault(cfg.Timeout),
		History:  hist.Messages,
		// Same default as the native devin hook: review (block) unless the user
		// opted into injection via the unified switch (OPENPE_HOOK_INJECT /
		// OPENPE_DEVIN_INJECT, resolved in config).
		Inject:           cfg.Inject.Devin,
		MaxContextTokens: cfg.MaxContextTokens,
	})
	if err != nil {
		return devinadapter.EncodeHookOutputOrFallback(stdout, devinadapter.HookError(true, err.Error(), cfg.Language))
	}
	claimOutcome = dedupOutcomeFor(output)
	output = deliverDevinBlock(cfg, output)
	output = applyDevinDisclosure(output, hist, histErr, cfg.Language)
	return devinadapter.EncodeHookOutputOrFallback(stdout, output)
}

// applyDevinDisclosure folds the non-silent notes into the hook output: the
// history disclosure (whether prior Devin-session context was included, and
// whether it contains a compaction summary) plus any enhancer content warnings
// (out-of-context numbers / undecided actions — they exist precisely to be
// seen before the user acts). Block outputs carry the notes in Reason,
// inject/skip outputs in SystemMessage.
func applyDevinDisclosure(output devinadapter.HookOutput, hist devinhistory.Result, histErr error, language string) devinadapter.HookOutput {
	notes := make([]string, 0, 1+len(output.Warnings))
	if note := historyDisclosure(hist.Messages, hist.Status, hist.SummaryCount, histErr, language); note != "" {
		notes = append(notes, note)
	}
	notes = append(notes, output.Warnings...)
	if len(notes) == 0 {
		return output
	}
	joined := strings.Join(notes, " ")
	if output.Decision == "block" {
		output.Reason = strings.TrimSpace(joined + " " + output.Reason)
	} else {
		output.SystemMessage = strings.TrimSpace(joined + " " + output.SystemMessage)
	}
	return output
}

// deliverDevinBlock copies the enhanced prompt to the clipboard and sets the
// block reason from the shared delivery.HookStatus, so every review-mode Devin
// path (the native devin hook and the imported claude/windsurf hooks) shows the
// same "blocked + copied, paste it" / "clipboard failed, see hook last" feedback
// as the Codex/Claude/Windsurf clients. It is a no-op for inject/skip outputs.
func deliverDevinBlock(cfg config.Config, output devinadapter.HookOutput) devinadapter.HookOutput {
	if output.Decision != "block" || strings.TrimSpace(output.PreviewPrompt) == "" {
		return output
	}
	result := delivery.Deliver(context.Background(), output.PreviewPrompt, configuredDeliveryOptions(cfg, "devin"))
	output.Reason = delivery.HookStatus(result, cfg.Language, hookLastPromptCommand("devin"))
	return output
}

// dedupOutcomeFor classifies a finished hook flight for the de-dup claim.
// Only a successful review block (an enhanced preview exists) is replayable
// by a loser; error blocks and empty results stay OutcomeUnknown so losers
// fall back to a plain skip instead of replaying a cache that does not match.
func dedupOutcomeFor(output devinadapter.HookOutput) hookdedup.Outcome {
	switch {
	case output.Decision == "block" && strings.TrimSpace(output.PreviewPrompt) != "":
		return hookdedup.OutcomeBlock
	case output.HookSpecificOutput != nil && strings.TrimSpace(output.HookSpecificOutput.AdditionalContext) != "":
		return hookdedup.OutcomeInject
	default:
		return hookdedup.OutcomeUnknown
	}
}

// replayDevinBlockedPrompt re-emits a review block for a de-dup loser whose
// winner blocked this same prompt moments ago. The 2026-07-03 incident showed
// a plain skip here betrays the interception: the user resubmits the same
// `pe` text right after the block (e.g. because the clipboard paste failed)
// and the raw trigger message sails through to the model. Replaying costs no
// model call: the winner's enhanced prompt is still in the delivery cache, so
// it is re-delivered (clipboard included) exactly like the original block.
func replayDevinBlockedPrompt(cfg config.Config, copyPreview bool, blockOutput string, terminalPreview bool, stdout io.Writer, stderr io.Writer) int {
	prompt, err := devinadapter.ReadLastPrompt()
	if err != nil || strings.TrimSpace(prompt) == "" {
		// Cache lost between the winner and this loser (rare): still block —
		// re-blocking without a preview beats re-sending the raw prompt.
		out := devinadapter.Block(strings.TrimSpace(localizedDedupReplayNote(cfg.Language) + " " + localizedDedupReplayCacheMiss(cfg.Language)))
		return emitDevinBlock(out, blockOutput, terminalPreview, stdout, stderr)
	}
	out := devinadapter.BlockPreview(devinadapter.PreviewReason("", cfg.Language), devinadapter.MarkdownPreview(prompt, cfg.Language), prompt)
	if copyPreview {
		out = deliverDevinBlock(cfg, out)
	}
	out.Reason = strings.TrimSpace(localizedDedupReplayNote(cfg.Language) + " " + out.Reason)
	return emitDevinBlock(out, blockOutput, terminalPreview, stdout, stderr)
}

// emitDevinBlock renders a block output the same way the native devin hook
// tail does: exit-2 stderr when requested, Devin-native JSON otherwise.
func emitDevinBlock(output devinadapter.HookOutput, blockOutput string, terminalPreview bool, stdout io.Writer, stderr io.Writer) int {
	if blockOutput == "stderr" {
		if terminalPreview {
			_ = devinadapter.WriteTerminalPreview(output.TerminalPreview)
		}
		fmt.Fprintln(stderr, output.Reason)
		return 2
	}
	return devinadapter.EncodeHookOutputOrFallback(stdout, output)
}

// localizedDedupReplayNote explains a de-dup replay: the same text was just
// enhanced and blocked, so this resubmission reuses that result instead of
// re-enhancing (or worse, passing the raw trigger message through).
func localizedDedupReplayNote(language string) string {
	if isEnglishLanguage(language) {
		return "openPE: duplicate submission within the de-dup window; reusing the just-generated enhancement (no re-enhancement)."
	}
	return "openPE：检测到去重窗口内重复提交同一消息，已复用刚生成的增强结果（未重新增强）。"
}

// localizedDedupReplayCacheMiss covers the degraded replay: the outcome said
// block but the cached enhancement could not be read back.
func localizedDedupReplayCacheMiss(language string) string {
	if isEnglishLanguage(language) {
		return "The cached enhancement could not be read; the original message was NOT submitted. Run `openpe devin hook last` to inspect the cache."
	}
	return "缓存的增强结果读取失败，原始消息未提交。可运行 openpe devin hook last 查看缓存。"
}

func runDevinHookInstall(args []string, stdout io.Writer, stderr io.Writer, getwd func() (string, error)) int {
	fs := flag.NewFlagSet("devin hook install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scope := fs.String("scope", "user", "hook scope: user or project")
	target := fs.String("path", "", "explicit hooks file path (.devin/hooks.v1.json or a devin config.json)")
	openpeBin := fs.String("openpe-bin", "", "openpe executable path; defaults to PATH lookup")
	envFile := fs.String("env-file", "", "dotenv file loaded by the hook; defaults to ~/.config/openpe/.env for user hooks or project .env for project hooks")
	hookTimeout := fs.Int("hook-timeout", 120, "Devin hook timeout in seconds")
	dryRun := fs.Bool("dry-run", false, "print the merged config without writing it")
	if ok, code := parseFlagSet(fs, args); !ok {
		return code
	}
	if *hookTimeout <= 0 {
		fmt.Fprintln(stderr, "hook-timeout must be positive")
		return 1
	}
	hooksPath, err := devinHooksPath(*scope, *target, getwd)
	if err != nil {
		fmt.Fprintf(stderr, "resolve devin hooks path: %v\n", err)
		return 1
	}
	bin, err := resolveOpenPEBin(*openpeBin)
	if err != nil {
		fmt.Fprintf(stderr, "resolve openpe binary: %v\n", err)
		return 1
	}
	hookEnvFile, err := devinHookEnvFile(*scope, *target, *envFile, getwd)
	if err != nil {
		fmt.Fprintf(stderr, "resolve hook env file: %v\n", err)
		return 1
	}
	var existing []byte
	if data, err := os.ReadFile(hooksPath); err == nil {
		existing = data
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(stderr, "read devin hooks config: %v\n", err)
		return 1
	}
	command := devinadapter.HookCommandForScope(bin, hookEnvFile, *scope)
	var merged []byte
	if devinConfigIsWrapped(hooksPath) {
		// ~/.config/devin/config.json (or config.local.json): hooks live under
		// the top-level "hooks" key; preserve all other config.
		merged, err = devinadapter.MergeConfigHooks(existing, command, *hookTimeout)
	} else {
		// .devin/hooks.v1.json: the hooks object is the entire file.
		merged, err = devinadapter.MergeStandaloneHooks(existing, command, *hookTimeout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "merge devin hooks config: %v\n", err)
		return 1
	}
	changed := !bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace(merged))
	if *dryRun {
		_, _ = stdout.Write(merged)
		return 0
	}
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		fmt.Fprintf(stderr, "create devin hooks directory: %v\n", err)
		return 1
	}
	if err := os.WriteFile(hooksPath, merged, 0o644); err != nil {
		fmt.Fprintf(stderr, "write devin hooks config: %v\n", err)
		return 1
	}
	if changed {
		fmt.Fprintf(stdout, "installed openPE Devin hook: %s\n", hooksPath)
	} else {
		fmt.Fprintf(stdout, "openPE Devin hook already installed: %s\n", hooksPath)
	}
	return 0
}

func devinHooksPath(scope string, target string, getwd func() (string, error)) (string, error) {
	if strings.TrimSpace(target) != "" {
		return filepath.Clean(target), nil
	}
	switch scope {
	case "project":
		cwd, err := getwd()
		if err != nil {
			return "", err
		}
		return devinadapter.ProjectHooksPath(cwd), nil
	case "user":
		path := devinadapter.UserConfigPath()
		if path == "" {
			return "", fmt.Errorf("resolve user home directory")
		}
		return path, nil
	default:
		return "", fmt.Errorf("unsupported scope %q", scope)
	}
}

// devinConfigIsWrapped reports whether the target path is a Devin config file
// (hooks nested under "hooks") rather than a standalone .devin/hooks.v1.json
// (hooks object is the whole file).
func devinConfigIsWrapped(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return base == "config.json" || base == "config.local.json"
}

func devinHookEnvFile(scope string, target string, value string, getwd func() (string, error)) (string, error) {
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
	svc := enhancer.NewService(provider)
	if cfg.Openace.Enabled {
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
		svc = enhancer.NewServiceWithContext(provider, contextProvider)
	}
	return svc.
		WithSystemPrompt(cfg.SystemPrompt).
		WithMessageStyle(messageStyleFromConfig(cfg)).
		WithLanguageGuard(languageGuardFromConfig(cfg)).
		WithContentWarnings(enhancer.ContentWarningsConfig{
			Enabled:      cfg.Warnings.Enabled,
			ExtraActions: cfg.Warnings.ExtraActions,
			NumMaxLen:    cfg.Warnings.NumMaxLen,
		}, cfg.Language).
		WithLogger(slog.Default()), nil
}

func messageStyleFromConfig(cfg config.Config) enhancer.MessageStyle {
	switch strings.ToLower(strings.TrimSpace(cfg.MessageStyle)) {
	case "hybrid":
		return enhancer.StyleHybrid
	case "structured":
		return enhancer.StyleStructured
	default:
		return enhancer.StyleFlatten
	}
}

// languageGuardFromConfig maps the config-layer guard switches onto the
// enhancer's LanguageGuardConfig (keeps internal/config enhancer-free).
func languageGuardFromConfig(cfg config.Config) enhancer.LanguageGuardConfig {
	return enhancer.LanguageGuardConfig{
		Enabled:  cfg.LanguageGuard.Enabled,
		Reanchor: cfg.LanguageGuard.Reanchor,
	}
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
