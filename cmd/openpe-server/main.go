package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/AoManoh/openpe/internal/config"
	"github.com/AoManoh/openpe/internal/enhancer"
	"github.com/AoManoh/openpe/internal/providers/openai"
	"github.com/AoManoh/openpe/internal/server"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "openpe-server: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg := config.Load()
	fs := flag.NewFlagSet("openpe-server", flag.ExitOnError)
	listenAddr := fs.String("listen", cfg.ListenAddr, "listen address")
	baseURL := fs.String("base-url", cfg.BaseURL, "OpenAI-compatible base URL")
	apiKey := fs.String("api-key", cfg.APIKey, "OpenAI-compatible API key")
	model := fs.String("model", cfg.Model, "OpenAI-compatible model")
	timeout := fs.Duration("timeout", cfg.Timeout, "provider timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	provider, err := openai.New(openai.Config{
		BaseURL: strings.TrimSpace(*baseURL),
		APIKey:  strings.TrimSpace(*apiKey),
		Model:   strings.TrimSpace(*model),
		Timeout: *timeout,
	})
	if err != nil {
		return fmt.Errorf("configure provider: %w", err)
	}
	httpServer := &http.Server{
		Addr:              strings.TrimSpace(*listenAddr),
		Handler:           server.New(enhancer.NewService(provider)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if httpServer.Addr == "" {
		httpServer.Addr = config.DefaultListenAddr
	}
	errCh := make(chan error, 1)
	go func() {
		fmt.Fprintf(os.Stderr, "openpe-server: listening on %s\n", httpServer.Addr)
		errCh <- httpServer.ListenAndServe()
	}()

	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM)
	select {
	case sig := <-signalCh:
		fmt.Fprintf(os.Stderr, "openpe-server: shutting down after %s\n", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(ctx)
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}
