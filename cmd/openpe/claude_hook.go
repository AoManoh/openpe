package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	claudeadapter "github.com/AoManoh/openpe/internal/adapters/claude"
	"github.com/AoManoh/openpe/internal/adapters/delivery"
	"github.com/AoManoh/openpe/internal/config"
	claudetranscript "github.com/AoManoh/openpe/internal/context/claudetranscript"
	"github.com/AoManoh/openpe/internal/context/histstatus"
	"github.com/AoManoh/openpe/internal/enhancer"
)

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

type claudeHookOptions struct {
	Client   string
	Mode     string
	Deadline time.Duration
	Provider providerOptions
}

type claudeHookRequest struct {
	Input        claudeadapter.HookInput
	OverrideCWD  string
	EffectiveCWD string
}

func runClaudeHookRun(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error)) int {
	cfg := config.Load()
	opts, ok, code := parseClaudeHookOptions(args, stderr, cfg)
	if !ok {
		return code
	}
	request, ok, code := prepareClaudeHookRequest(stdin, stderr, getwd, cfg)
	if !ok {
		return code
	}
	service, err := newConfiguredEnhancerService(newProvider, cfg, opts.Provider)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", localizedEnhanceFailure(err.Error(), cfg.Language))
		return 2
	}
	// When the Devin CLI imports and runs this Claude hook, render Devin-native
	// output (de-duplicated against sibling openPE hooks) with the Devin
	// session's history and cache namespace instead of Claude's.
	if runningUnderDevin() {
		return renderDevinHook(cfg, service, request.Input.Prompt, request.Input.CWD, opts.Deadline, stdout, stderr)
	}
	history, histStatus, histErr := claudeTranscriptHistory(request.Input.TranscriptPath, request.EffectiveCWD, cfg)
	output, err := claudeadapter.HandleHook(context.Background(), service, request.Input, claudeadapter.HookOptions{
		Client:           opts.Client,
		Mode:             opts.Mode,
		CWD:              request.OverrideCWD,
		Language:         cfg.Language,
		Timeout:          timeoutOrDefault(opts.Provider.Timeout),
		History:          history,
		Inject:           cfg.Inject.Claude,
		MaxContextTokens: cfg.MaxContextTokens,
		CacheDir:         cfg.Delivery.CacheDir,
	})
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", localizedEnhanceFailure(err.Error(), cfg.Language))
		return 2
	}
	return emitClaudeHookOutput(output, history, histStatus, histErr, cfg, stdout, stderr)
}

func parseClaudeHookOptions(args []string, stderr io.Writer, cfg config.Config) (claudeHookOptions, bool, int) {
	fs := flag.NewFlagSet("claude hook run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	client := fs.String("client", "claude-code", "target client name")
	mode := fs.String("mode", "agent", "prompt mode")
	deadline := fs.Duration("deadline", cfg.HookDeadline, "overall deadline when this hook runs under Devin")
	providerFlags := bindProviderFlags(fs, cfg)
	if ok, code := parseFlagSet(fs, args); !ok {
		return claudeHookOptions{}, false, code
	}
	if *deadline <= 0 {
		fmt.Fprintln(stderr, "deadline must be positive")
		return claudeHookOptions{}, false, 2
	}
	return claudeHookOptions{
		Client:   *client,
		Mode:     *mode,
		Deadline: *deadline,
		Provider: providerFlags.options(cfg),
	}, true, 0
}

func prepareClaudeHookRequest(stdin io.Reader, stderr io.Writer, getwd func() (string, error), cfg config.Config) (claudeHookRequest, bool, int) {
	input, err := claudeadapter.DecodeHookInput(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", localizedInvalidClaudeHookInput(err, cfg.Language))
		return claudeHookRequest{}, false, 0
	}
	if !claudeadapter.ShouldHandleHook(input) {
		return claudeHookRequest{}, false, 0
	}
	overrideCWD := ""
	effectiveCWD := strings.TrimSpace(input.CWD)
	if effectiveCWD == "" {
		workingDir, err := getwd()
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", localizedEnhanceFailure(fmt.Sprintf("get cwd: %v", err), cfg.Language))
			return claudeHookRequest{}, false, 2
		}
		overrideCWD = workingDir
		effectiveCWD = workingDir
	}
	return claudeHookRequest{
		Input:        input,
		OverrideCWD:  overrideCWD,
		EffectiveCWD: effectiveCWD,
	}, true, 0
}

func emitClaudeHookOutput(output claudeadapter.HookOutput, history []enhancer.Message, histStatus histstatus.Status, histErr error, cfg config.Config, stdout io.Writer, stderr io.Writer) int {
	// Inject mode (OPENPE_HOOK_INJECT / OPENPE_CLAUDE_INJECT): emit exit-0 JSON
	// with additionalContext (Claude CLI injects it via a system-reminder) plus
	// the non-silent history disclosure; cache the prompt for `claude hook last`.
	if output.Injected {
		_, _ = delivery.SaveWithOptions("claude", output.PreviewPrompt, cfg.Language, configuredDeliveryOptions(cfg, "claude"))
		if note := disclosureNotes(history, histStatus, 0, histErr, output.Warnings, cfg.Language); note != "" {
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
	// (and if not, why / or that reading failed) plus any enhancer content
	// warnings — they must be read before the user pastes the enhancement.
	if note := disclosureNotes(history, histStatus, 0, histErr, output.Warnings, cfg.Language); note != "" {
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
