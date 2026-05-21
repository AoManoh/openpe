package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/AoManoh/openpe/internal/config"
	"github.com/AoManoh/openpe/internal/enhancer"
	"github.com/AoManoh/openpe/internal/providers/openai"
)

type providerFactory func(openai.Config) (enhancer.Provider, error)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, func(cfg openai.Config) (enhancer.Provider, error) {
		return openai.New(cfg)
	}, os.Getwd))
}

func run(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error)) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
	case "enhance":
		return runEnhance(args[1:], stdin, stdout, stderr, newProvider, getwd)
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

func timeoutOrDefault(value time.Duration) time.Duration {
	if value <= 0 {
		return config.DefaultTimeout
	}
	return value
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: openpe enhance [--prompt text] [--json] [--client name] [--mode name]")
}
