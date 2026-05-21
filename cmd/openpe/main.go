package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	codexadapter "github.com/AoManoh/openpe/internal/adapters/codex"
	"github.com/AoManoh/openpe/internal/config"
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
	baseURL := fs.String("base-url", cfg.BaseURL, "OpenAI-compatible base URL")
	apiKey := fs.String("api-key", cfg.APIKey, "OpenAI-compatible API key")
	model := fs.String("model", cfg.Model, "OpenAI-compatible model")
	timeout := fs.Duration("timeout", cfg.Timeout, "provider timeout")
	if err := fs.Parse(args); err != nil {
		return 2
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
		BaseURL: strings.TrimSpace(*baseURL),
		APIKey:  strings.TrimSpace(*apiKey),
		Model:   strings.TrimSpace(*model),
		Timeout: *timeout,
	})
	if err != nil {
		fmt.Fprintf(stderr, "configure provider: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeoutOrDefault(*timeout))
	defer cancel()
	resp, err := enhancer.NewService(provider).Enhance(ctx, enhancer.Request{
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
	baseURL := fs.String("base-url", cfg.BaseURL, "OpenAI-compatible base URL")
	apiKey := fs.String("api-key", cfg.APIKey, "OpenAI-compatible API key")
	model := fs.String("model", cfg.Model, "OpenAI-compatible model")
	timeout := fs.Duration("timeout", cfg.Timeout, "provider timeout")
	if err := fs.Parse(args); err != nil {
		return 2
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
		BaseURL: strings.TrimSpace(*baseURL),
		APIKey:  strings.TrimSpace(*apiKey),
		Model:   strings.TrimSpace(*model),
		Timeout: *timeout,
	})
	if err != nil {
		fmt.Fprintf(stderr, "configure provider: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeoutOrDefault(*timeout))
	defer cancel()
	resp, err := enhancer.NewService(provider).Enhance(ctx, enhancer.Request{
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
	case "-h", "--help", "help":
		printCodexHookUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown codex hook command: %s\n", args[0])
		printCodexHookUsage(stderr)
		return 2
	}
}

func runCodexHookRun(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error)) int {
	cfg := config.Load()
	fs := flag.NewFlagSet("codex hook run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	client := fs.String("client", "codex", "target client name")
	mode := fs.String("mode", "agent", "prompt mode")
	auto := fs.Bool("auto", false, "enhance every prompt and inject it as additional context")
	baseURL := fs.String("base-url", cfg.BaseURL, "OpenAI-compatible base URL")
	apiKey := fs.String("api-key", cfg.APIKey, "OpenAI-compatible API key")
	model := fs.String("model", cfg.Model, "OpenAI-compatible model")
	timeout := fs.Duration("timeout", cfg.Timeout, "provider timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	input, err := codexadapter.DecodeHookInput(stdin)
	if err != nil {
		return codexadapter.EncodeHookOutputOrFallback(stdout, codexadapter.Skip(fmt.Sprintf("openPE skipped prompt enhancement: invalid hook input: %v", err)))
	}
	manual, shouldHandle := codexadapter.ShouldHandleHook(input, *auto)
	if !shouldHandle {
		return 0
	}
	overrideCWD := ""
	if strings.TrimSpace(input.CWD) == "" {
		workingDir, err := getwd()
		if err != nil {
			return codexadapter.EncodeHookOutputOrFallback(stdout, codexadapter.HookError(manual, fmt.Sprintf("get cwd: %v", err)))
		}
		overrideCWD = workingDir
	}
	provider, err := newProvider(openai.Config{
		BaseURL: strings.TrimSpace(*baseURL),
		APIKey:  strings.TrimSpace(*apiKey),
		Model:   strings.TrimSpace(*model),
		Timeout: *timeout,
	})
	if err != nil {
		return codexadapter.EncodeHookOutputOrFallback(stdout, codexadapter.HookError(manual, fmt.Sprintf("configure provider: %v", err)))
	}
	output, err := codexadapter.HandleHook(context.Background(), enhancer.NewService(provider), input, codexadapter.HookOptions{
		Client:  *client,
		Mode:    *mode,
		Auto:    *auto,
		CWD:     overrideCWD,
		Timeout: timeoutOrDefault(*timeout),
	})
	if err != nil {
		return codexadapter.EncodeHookOutputOrFallback(stdout, codexadapter.HookError(manual, err.Error()))
	}
	return codexadapter.EncodeHookOutputOrFallback(stdout, output)
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
	if err := fs.Parse(args); err != nil {
		return 2
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
	command := codexadapter.HookCommand(bin, hookEnvFile)
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

func runCommand(ctx context.Context, name string, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func timeoutOrDefault(value time.Duration) time.Duration {
	if value <= 0 {
		return config.DefaultTimeout
	}
	return value
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  openpe enhance [--prompt text] [--json] [--client name] [--mode name]")
	fmt.Fprintln(w, "  openpe codex [--prompt text] [--dry-run] [--codex-arg arg]...")
	fmt.Fprintln(w, "  openpe codex hook install [--scope project|user]")
	fmt.Fprintln(w, "  openpe codex hook run")
}

func printCodexHookUsage(w io.Writer) {
	fmt.Fprintln(w, "usage:")
	fmt.Fprintln(w, "  openpe codex hook install [--scope project|user] [--path hooks.json] [--openpe-bin path]")
	fmt.Fprintln(w, "  openpe codex hook run [--auto]")
}

func envOrDefault(name string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

type repeatedFlag []string

func (f *repeatedFlag) String() string {
	return strings.Join(*f, " ")
}

func (f *repeatedFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}
