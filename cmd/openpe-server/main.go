package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/AoManoh/openpe/internal/config"
	openacectx "github.com/AoManoh/openpe/internal/context/openace"
	"github.com/AoManoh/openpe/internal/enhancer"
	"github.com/AoManoh/openpe/internal/integration"
	"github.com/AoManoh/openpe/internal/providers/openai"
	"github.com/AoManoh/openpe/internal/server"
)

// Version is the build identifier exposed via GET /v1/info and the
// lifecycle descriptor. Override at build time with
//
//	go build -ldflags "-X main.Version=v0.2.0" ./cmd/openpe-server
var Version = "dev"

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

	listen := strings.TrimSpace(*listenAddr)
	if listen == "" {
		listen = config.DefaultListenAddr
	}

	// Lifecycle: opt-in descriptor + ephemeral token for IDE installers.
	// When disabled (default), behaviour is identical to the historical
	// no-handshake openpe-server used by hook / CLI consumers.
	token := cfg.Server.Token
	tokenSource := "OPENPE_SERVER_TOKEN"
	var descriptorPath string
	if cfg.Server.LifecycleEnabled {
		if token == "" {
			generated, err := integration.GenerateToken()
			if err != nil {
				return fmt.Errorf("generate ephemeral server token: %w", err)
			}
			token = generated
			tokenSource = "ephemeral (lifecycle auto-generated)"
		}
		descriptorPath = cfg.Server.DescriptorFile
		if descriptorPath == "" {
			descriptorPath, err = integration.DefaultDescriptorPath()
			if err != nil {
				return fmt.Errorf("resolve descriptor path: %w", err)
			}
		}
		descriptor := integration.NewLocalServerDescriptor(deriveBaseURL(listen), token, os.Getpid(), Version)
		if err := integration.WriteDescriptor(descriptorPath, descriptor); err != nil {
			return fmt.Errorf("write descriptor %s: %w", descriptorPath, err)
		}
		fmt.Fprintf(os.Stderr, "openpe-server: descriptor written to %s (mode 0600)\n", descriptorPath)
		defer func() {
			if removeErr := integration.RemoveDescriptor(descriptorPath); removeErr != nil {
				fmt.Fprintf(os.Stderr, "openpe-server: cleanup descriptor %s: %v\n", descriptorPath, removeErr)
			}
		}()
	}

	httpServer := &http.Server{
		Addr: listen,
		Handler: server.NewWithOptions(service, server.Options{
			Token: token,
			CORS:  server.CORSOptions{AllowedOrigins: cfg.Server.CORSOrigins},
			Info: server.ServerInfo{
				Version:    Version,
				StartedAt:  time.Now().UTC(),
				ListenAddr: listen,
			},
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	authStatus := "disabled (set OPENPE_SERVER_TOKEN to enable bearer auth)"
	if token != "" {
		authStatus = fmt.Sprintf("enabled via %s", tokenSource)
	}
	corsStatus := "disabled"
	if len(cfg.Server.CORSOrigins) > 0 {
		corsStatus = fmt.Sprintf("enabled for %s", strings.Join(cfg.Server.CORSOrigins, ", "))
	}
	lifecycleStatus := "disabled"
	if cfg.Server.LifecycleEnabled {
		lifecycleStatus = fmt.Sprintf("descriptor=%s", descriptorPath)
	}

	errCh := make(chan error, 1)
	go func() {
		fmt.Fprintf(os.Stderr,
			"openpe-server: listening on %s (version=%s; auth=%s; cors=%s; lifecycle=%s)\n",
			listen, Version, authStatus, corsStatus, lifecycleStatus)
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

// deriveBaseURL converts a "host:port" listen address into the base URL an
// IDE installer running on the same host can use. Wildcard / unspecified
// hosts (0.0.0.0, ::, empty) are rewritten to 127.0.0.1 because the
// installer always lives on the loopback path.
func deriveBaseURL(listenAddr string) string {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil || host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
		if err != nil {
			port = "18980"
		}
	}
	return "http://" + net.JoinHostPort(host, port)
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
