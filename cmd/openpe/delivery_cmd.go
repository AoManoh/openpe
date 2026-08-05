package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/AoManoh/openpe/internal/adapters/clipboard"
	"github.com/AoManoh/openpe/internal/adapters/delivery"
	"github.com/AoManoh/openpe/internal/config"
)

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
		path, err := resolveDeliveryPath(client, *promptOnly, opts)
		if err != nil {
			fmt.Fprintf(stderr, "resolve cache path: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, path)
		return 0
	}
	content, err := readDeliveryContent(client, *promptOnly, opts)
	if err != nil {
		fmt.Fprintf(stderr, "read cached content: %v\n", err)
		return 1
	}
	fmt.Fprint(stdout, content)
	return 0
}

func resolveDeliveryPath(client string, promptOnly bool, opts delivery.Options) (string, error) {
	if opts.CacheDir != "" {
		if promptOnly {
			return delivery.LastPromptPathWithOptions(client, opts)
		}
		return delivery.LastPreviewPathWithOptions(client, opts)
	}
	if promptOnly {
		return delivery.LastPromptPath(client)
	}
	return delivery.LastPreviewPath(client)
}

func readDeliveryContent(client string, promptOnly bool, opts delivery.Options) (string, error) {
	if opts.CacheDir != "" {
		if promptOnly {
			return delivery.ReadLastPromptWithOptions(client, opts)
		}
		return delivery.ReadLastPreviewWithOptions(client, opts)
	}
	if promptOnly {
		return delivery.ReadLastPrompt(client)
	}
	return delivery.ReadLastPreview(client)
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
