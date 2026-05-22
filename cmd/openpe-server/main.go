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
	openacectx "github.com/AoManoh/openpe/internal/context/openace"
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
	service, err := newEnhancerService(provider, cfg)
	if err != nil {
		return fmt.Errorf("configure context provider: %w", err)
	}
	httpServer := &http.Server{
		Addr: strings.TrimSpace(*listenAddr),
		Handler: server.NewWithOptions(service, server.Options{
			Token: cfg.Server.Token,
			CORS:  server.CORSOptions{AllowedOrigins: cfg.Server.CORSOrigins},
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if httpServer.Addr == "" {
		httpServer.Addr = config.DefaultListenAddr
	}
	authStatus := "disabled (set OPENPE_SERVER_TOKEN to enable bearer auth)"
	if cfg.Server.Token != "" {
		authStatus = "enabled (bearer token required for /v1/*)"
	}
	corsStatus := "disabled"
	if len(cfg.Server.CORSOrigins) > 0 {
		corsStatus = fmt.Sprintf("enabled for %s", strings.Join(cfg.Server.CORSOrigins, ", "))
	}
	errCh := make(chan error, 1)
	go func() {
		fmt.Fprintf(os.Stderr, "openpe-server: listening on %s (auth: %s; cors: %s)\n", httpServer.Addr, authStatus, corsStatus)
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
