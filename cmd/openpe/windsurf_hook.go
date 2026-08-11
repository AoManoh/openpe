package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/AoManoh/openpe/internal/adapters/delivery"
	windsurfadapter "github.com/AoManoh/openpe/internal/adapters/windsurf"
	"github.com/AoManoh/openpe/internal/config"
)

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

type windsurfHookOptions struct {
	Client   string
	Mode     string
	Deadline time.Duration
	Provider providerOptions
}

type windsurfHookRequest struct {
	Input       windsurfadapter.HookInput
	OverrideCWD string
}

func runWindsurfHookRun(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer, newProvider providerFactory, getwd func() (string, error)) int {
	cfg := config.Load()
	opts, ok, code := parseWindsurfHookOptions(args, stderr, cfg)
	if !ok {
		return code
	}
	request, ok, code := prepareWindsurfHookRequest(stdin, stderr, getwd, cfg)
	if !ok {
		return code
	}
	service, err := newConfiguredEnhancerService(newProvider, cfg, opts.Provider)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", localizedEnhanceFailure(err.Error(), cfg.Language))
		return 2
	}
	// When the Devin CLI imports and runs this Windsurf hook, render Devin-native
	// output (de-duplicated against sibling openPE hooks) with the Devin
	// session's history and cache namespace instead of Windsurf's.
	if runningUnderDevin() {
		return renderDevinHook(cfg, service, request.Input.ToolInfo.UserPrompt, request.Input.CWD, opts.Deadline, stdout, stderr)
	}
	output, err := windsurfadapter.HandleHook(context.Background(), service, request.Input, windsurfadapter.HookOptions{
		Client:           opts.Client,
		Mode:             opts.Mode,
		CWD:              request.OverrideCWD,
		Language:         cfg.Language,
		Timeout:          timeoutOrDefault(opts.Provider.Timeout),
		MaxContextTokens: cfg.MaxContextTokens,
		CacheDir:         cfg.Delivery.CacheDir,
	})
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", localizedEnhanceFailure(err.Error(), cfg.Language))
		return 2
	}
	return emitWindsurfHookOutput(output, cfg, stderr)
}

func parseWindsurfHookOptions(args []string, stderr io.Writer, cfg config.Config) (windsurfHookOptions, bool, int) {
	fs := flag.NewFlagSet("windsurf hook run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	client := fs.String("client", "windsurf", "target client name")
	mode := fs.String("mode", "cascade", "prompt mode")
	deadline := fs.Duration("deadline", cfg.HookDeadline, "overall deadline when this hook runs under Devin")
	providerFlags := bindProviderFlags(fs, cfg)
	if ok, code := parseFlagSet(fs, args); !ok {
		return windsurfHookOptions{}, false, code
	}
	if *deadline <= 0 {
		fmt.Fprintln(stderr, "deadline must be positive")
		return windsurfHookOptions{}, false, 2
	}
	return windsurfHookOptions{
		Client:   *client,
		Mode:     *mode,
		Deadline: *deadline,
		Provider: providerFlags.options(cfg),
	}, true, 0
}

func prepareWindsurfHookRequest(stdin io.Reader, stderr io.Writer, getwd func() (string, error), cfg config.Config) (windsurfHookRequest, bool, int) {
	input, err := windsurfadapter.DecodeHookInput(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "%s\n", localizedInvalidWindsurfHookInput(err, cfg.Language))
		return windsurfHookRequest{}, false, 0
	}
	if !windsurfadapter.ShouldHandleHook(input) {
		return windsurfHookRequest{}, false, 0
	}
	overrideCWD := ""
	if strings.TrimSpace(input.CWD) == "" {
		workingDir, err := getwd()
		if err != nil {
			fmt.Fprintf(stderr, "%s\n", localizedEnhanceFailure(fmt.Sprintf("get cwd: %v", err), cfg.Language))
			return windsurfHookRequest{}, false, 2
		}
		overrideCWD = workingDir
	}
	return windsurfHookRequest{Input: input, OverrideCWD: overrideCWD}, true, 0
}

func emitWindsurfHookOutput(output windsurfadapter.HookOutput, cfg config.Config, stderr io.Writer) int {
	if strings.TrimSpace(output.PreviewPrompt) == "" {
		return 0
	}
	result := delivery.Deliver(context.Background(), output.PreviewPrompt, configuredDeliveryOptions(cfg, "windsurf"))
	status := delivery.HookStatus(result, cfg.Language, hookLastPromptCommand("windsurf"))
	if result.CopyError != nil && cfg.Delivery.WindsurfPromptFallback {
		status = delivery.AppendPromptFallback(status, output.PreviewPrompt, cfg.Language)
	}
	// Enhancer content warnings must be visible before the user pastes the
	// enhancement (Windsurf has no history collector, so this is the whole
	// disclosure).
	if joined := strings.TrimSpace(strings.Join(output.Warnings, " ")); joined != "" {
		status = joined + "\n" + status
	}
	fmt.Fprintln(stderr, status)
	return 2
}

func runWindsurfHookLast(args []string, stdout io.Writer, stderr io.Writer) int {
	return runDeliveryLast("windsurf hook last", "windsurf", args, stdout, stderr)
}
