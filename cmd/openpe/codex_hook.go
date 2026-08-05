package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	codexadapter "github.com/AoManoh/openpe/internal/adapters/codex"
	"github.com/AoManoh/openpe/internal/adapters/delivery"
	"github.com/AoManoh/openpe/internal/config"
	codexhistory "github.com/AoManoh/openpe/internal/context/codexhistory"
	"github.com/AoManoh/openpe/internal/context/histstatus"
	"github.com/AoManoh/openpe/internal/enhancer"
)

type codexRunOptions struct {
	Prompt           string
	DryRun           bool
	Client           string
	Mode             string
	CWD              string
	CodexBin         string
	CodexArgs        []string
	Positional       []string
	Provider         providerOptions
	MaxContextTokens int
}

func runCodex(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error), runCmd commandRunner) int {
	if len(args) > 0 && args[0] == "hook" {
		return runCodexHook(args[1:], stdin, stdout, stderr, newProvider, getwd)
	}
	if len(args) > 0 && args[0] == "last" {
		return runCodexHookLast(args[1:], stdout, stderr)
	}
	cfg := config.Load()
	opts, ok, code := parseCodexRunOptions(args, stderr, cfg)
	if !ok {
		return code
	}
	rawPrompt, err := readCodexPrompt(opts, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "read stdin: %v\n", err)
		return 1
	}
	if rawPrompt == "" {
		fmt.Fprintln(stderr, "prompt is required")
		return 1
	}
	cwd, err := workingDirectory(opts.CWD, getwd)
	if err != nil {
		fmt.Fprintf(stderr, "get cwd: %v\n", err)
		return 1
	}
	service, err := newConfiguredEnhancerService(newProvider, cfg, opts.Provider)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	resp, err := enhanceCodexPrompt(service, rawPrompt, cwd, opts)
	if err != nil {
		fmt.Fprintf(stderr, "enhance prompt: %v\n", err)
		return 1
	}
	return deliverCodexRun(resp.EnhancedPrompt, cwd, opts, stdout, stderr, runCmd)
}

func parseCodexRunOptions(args []string, stderr io.Writer, cfg config.Config) (codexRunOptions, bool, int) {
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
	providerFlags := bindProviderFlags(fs, cfg)
	if ok, code := parseFlagSet(fs, args); !ok {
		return codexRunOptions{}, false, code
	}
	return codexRunOptions{
		Prompt:           *prompt,
		DryRun:           *dryRun,
		Client:           *client,
		Mode:             *mode,
		CWD:              *cwd,
		CodexBin:         *codexBin,
		CodexArgs:        append([]string(nil), codexArgs...),
		Positional:       append([]string(nil), fs.Args()...),
		Provider:         providerFlags.options(cfg),
		MaxContextTokens: cfg.MaxContextTokens,
	}, true, 0
}

func readCodexPrompt(opts codexRunOptions, stdin io.Reader) (string, error) {
	rawPrompt := strings.TrimSpace(opts.Prompt)
	if rawPrompt == "" && len(opts.Positional) > 0 {
		rawPrompt = strings.TrimSpace(strings.Join(opts.Positional, " "))
	}
	if rawPrompt != "" {
		return rawPrompt, nil
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func enhanceCodexPrompt(service *enhancer.Service, rawPrompt string, cwd string, opts codexRunOptions) (enhancer.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeoutOrDefault(opts.Provider.Timeout))
	defer cancel()
	return service.Enhance(ctx, enhancer.Request{
		Prompt:  rawPrompt,
		Client:  opts.Client,
		CWD:     cwd,
		Mode:    opts.Mode,
		Options: enhancer.Options{MaxContextTokens: opts.MaxContextTokens},
	})
}

func deliverCodexRun(enhancedPrompt string, cwd string, opts codexRunOptions, stdout io.Writer, stderr io.Writer, runCmd commandRunner) int {
	if opts.DryRun {
		fmt.Fprintln(stdout, enhancedPrompt)
		return 0
	}
	commandArgs := []string{"exec", "-C", cwd}
	commandArgs = append(commandArgs, opts.CodexArgs...)
	commandArgs = append(commandArgs, "-")
	if err := runCmd(context.Background(), opts.CodexBin, commandArgs, strings.NewReader(enhancedPrompt+"\n"), stdout, stderr); err != nil {
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

type codexHookOptions struct {
	Client          string
	Mode            string
	Auto            bool
	BlockOutput     string
	TerminalPreview bool
	CopyPreview     bool
	HookScope       string
	Provider        providerOptions
}

type codexHookRequest struct {
	Input        codexadapter.HookInput
	Manual       bool
	OverrideCWD  string
	EffectiveCWD string
}

type codexHookExecution struct {
	Output        codexadapter.HookOutput
	History       []enhancer.Message
	HistoryStatus histstatus.Status
	HistoryErr    error
}

func runCodexHookRun(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error)) int {
	cfg := config.Load()
	opts, ok, code := parseCodexHookOptions(args, stderr, cfg)
	if !ok {
		return code
	}
	request, ok, code := prepareCodexHookRequest(stdin, stdout, getwd, cfg, opts)
	if !ok {
		return code
	}
	execution, err := executeCodexHook(request, opts, cfg, newProvider)
	if err != nil {
		return codexadapter.EncodeHookOutputOrFallback(stdout, codexadapter.HookError(request.Manual, err.Error(), cfg.Language))
	}
	return emitCodexHookExecution(execution, opts, cfg, stdout, stderr)
}

func parseCodexHookOptions(args []string, stderr io.Writer, cfg config.Config) (codexHookOptions, bool, int) {
	fs := flag.NewFlagSet("codex hook run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	client := fs.String("client", "codex", "target client name")
	mode := fs.String("mode", "agent", "prompt mode")
	auto := fs.Bool("auto", false, "enhance every prompt and inject it as additional context")
	blockOutput := fs.String("block-output", "json", "preview block output: json or stderr")
	terminalPreview := fs.Bool("terminal-preview", envBoolOrDefault("OPENPE_CODEX_TERMINAL_PREVIEW", false), "experimental: write full Markdown preview directly to /dev/tty when blocking")
	copyPreview := fs.Bool("copy-preview", envBoolOrDefault("OPENPE_CODEX_COPY_PREVIEW", false), "copy enhanced prompt to the system clipboard when blocking")
	hookScope := fs.String("hook-scope", envOrDefault("OPENPE_HOOK_SCOPE", ""), "hook scope for duplicate suppression: user or project")
	_ = fs.Duration("deadline", cfg.HookDeadline, "reserved overall deadline when this hook runs under a host aggregator")
	providerFlags := bindProviderFlags(fs, cfg)
	if ok, code := parseFlagSet(fs, args); !ok {
		return codexHookOptions{}, false, code
	}
	return codexHookOptions{
		Client:          *client,
		Mode:            *mode,
		Auto:            *auto,
		BlockOutput:     *blockOutput,
		TerminalPreview: *terminalPreview,
		CopyPreview:     *copyPreview,
		HookScope:       *hookScope,
		Provider:        providerFlags.options(cfg),
	}, true, 0
}

func prepareCodexHookRequest(stdin io.Reader, stdout io.Writer, getwd func() (string, error), cfg config.Config, opts codexHookOptions) (codexHookRequest, bool, int) {
	input, err := codexadapter.DecodeHookInput(stdin)
	if err != nil {
		code := codexadapter.EncodeHookOutputOrFallback(stdout, codexadapter.Skip(localizedInvalidCodexHookInput(err, cfg.Language)))
		return codexHookRequest{}, false, code
	}
	manual, shouldHandle := codexadapter.ShouldHandleHook(input, opts.Auto)
	if !shouldHandle {
		return codexHookRequest{}, false, 0
	}
	overrideCWD := ""
	if strings.TrimSpace(input.CWD) == "" {
		workingDir, err := getwd()
		if err != nil {
			code := codexadapter.EncodeHookOutputOrFallback(stdout, codexadapter.HookError(manual, fmt.Sprintf("get cwd: %v", err), cfg.Language))
			return codexHookRequest{}, false, code
		}
		overrideCWD = workingDir
	}
	effectiveCWD := strings.TrimSpace(input.CWD)
	if overrideCWD != "" {
		effectiveCWD = overrideCWD
	}
	if codexadapter.ShouldSkipDuplicateHook(opts.HookScope, os.Getenv("OPENPE_ENV_FILE"), effectiveCWD) {
		return codexHookRequest{}, false, 0
	}
	return codexHookRequest{
		Input:        input,
		Manual:       manual,
		OverrideCWD:  overrideCWD,
		EffectiveCWD: effectiveCWD,
	}, true, 0
}

func executeCodexHook(request codexHookRequest, opts codexHookOptions, cfg config.Config, newProvider providerFactory) (codexHookExecution, error) {
	history, histStatus, histErr := codexSessionHistory(request.Input.Prompt, request.EffectiveCWD, cfg)
	service, err := newConfiguredEnhancerService(newProvider, cfg, opts.Provider)
	if err != nil {
		return codexHookExecution{}, err
	}
	output, err := codexadapter.HandleHook(context.Background(), service, request.Input, codexadapter.HookOptions{
		Client:           opts.Client,
		Mode:             opts.Mode,
		Auto:             opts.Auto,
		CWD:              request.OverrideCWD,
		Language:         cfg.Language,
		Timeout:          timeoutOrDefault(opts.Provider.Timeout),
		History:          history,
		Inject:           cfg.Inject.Codex,
		MaxContextTokens: cfg.MaxContextTokens,
		CacheDir:         cfg.Delivery.CacheDir,
	})
	if err != nil {
		return codexHookExecution{}, err
	}
	return codexHookExecution{
		Output:        output,
		History:       history,
		HistoryStatus: histStatus,
		HistoryErr:    histErr,
	}, nil
}

func emitCodexHookExecution(execution codexHookExecution, opts codexHookOptions, cfg config.Config, stdout io.Writer, stderr io.Writer) int {
	output := execution.Output
	if output.Decision == "block" && opts.CopyPreview && output.PreviewPrompt != "" {
		result := delivery.Deliver(context.Background(), output.PreviewPrompt, configuredDeliveryOptions(cfg, "codex"))
		output.Reason = delivery.HookStatus(result, cfg.Language, hookLastPromptCommand("codex"))
	}
	// Non-silent disclosure: always state whether prior context was included
	// (and if not, why / or that reading failed), so a history-less result is
	// never mistaken for a context-aware one.
	if note := historyDisclosure(execution.History, execution.HistoryStatus, 0, execution.HistoryErr, cfg.Language); note != "" {
		if output.Decision == "block" {
			output.Reason = strings.TrimSpace(note + " " + output.Reason)
		} else {
			output.SystemMessage = strings.TrimSpace(note + " " + output.SystemMessage)
		}
	}
	if output.Decision == "block" && opts.BlockOutput == "stderr" {
		if opts.TerminalPreview {
			_ = codexadapter.WriteTerminalPreview(output.TerminalPreview)
		}
		fmt.Fprintln(stderr, output.Reason)
		return 2
	}
	if output.Decision == "block" && opts.BlockOutput != "json" {
		return codexadapter.EncodeHookOutputOrFallback(stdout, codexadapter.HookError(true, fmt.Sprintf("unsupported block-output %q", opts.BlockOutput), cfg.Language))
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
