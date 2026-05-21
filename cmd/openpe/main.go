package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

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
