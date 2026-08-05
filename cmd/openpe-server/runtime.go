package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/AoManoh/openpe/internal/config"
	"github.com/AoManoh/openpe/internal/enhancer"
	"github.com/AoManoh/openpe/internal/integration"
	"github.com/AoManoh/openpe/internal/providers"
	"github.com/AoManoh/openpe/internal/server"
)

type serverOptions struct {
	Config  config.Config
	Listen  string
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
}

type serverLifecycle struct {
	Enabled     bool
	Token       string
	TokenSource string
}

type serverBinding struct {
	Listener       net.Listener
	Address        string
	DescriptorPath string
	cleanup        func()
}

type serverStatus struct {
	Auth      string
	CORS      string
	Lifecycle string
}

func parseServerOptions(args []string, stdout io.Writer) (serverOptions, bool, error) {
	cfg := config.Load()
	fs := flag.NewFlagSet("openpe-server", flag.ContinueOnError)
	fs.SetOutput(stdout)
	listenAddr := configStringFlag(fs, "listen", "listen address (defaults to OPENPE_LISTEN_ADDR or 127.0.0.1:18980)")
	baseURL := configStringFlag(fs, "base-url", "OpenAI-compatible base URL (defaults to OPENPE_BASE_URL)")
	apiKey := configStringFlag(fs, "api-key", "OpenAI-compatible API key (defaults to OPENPE_API_KEY)")
	model := configStringFlag(fs, "model", "OpenAI-compatible model (defaults to OPENPE_MODEL)")
	timeout := fs.Duration("timeout", cfg.Timeout, "provider timeout")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return serverOptions{}, false, nil
		}
		return serverOptions{}, false, err
	}
	listen := listenAddr.ValueOrDefault(cfg.ListenAddr)
	if listen == "" {
		listen = config.DefaultListenAddr
	}
	return serverOptions{
		Config:  cfg,
		Listen:  listen,
		BaseURL: baseURL.ValueOrDefault(cfg.BaseURL),
		APIKey:  apiKey.ValueOrDefault(cfg.APIKey),
		Model:   model.ValueOrDefault(cfg.Model),
		Timeout: *timeout,
	}, true, nil
}

func prepareLifecycle(opts serverOptions) (serverLifecycle, error) {
	lifecycle := serverLifecycle{
		Enabled:     opts.Config.Server.LifecycleEnabled,
		Token:       opts.Config.Server.Token,
		TokenSource: "OPENPE_SERVER_TOKEN",
	}
	// Lifecycle: opt-in descriptor + ephemeral token for IDE installers.
	// When disabled (default), behaviour is identical to the historical
	// no-handshake openpe-server used by hook / CLI consumers.
	if lifecycle.Enabled && lifecycle.Token == "" {
		generated, err := integration.GenerateToken()
		if err != nil {
			return serverLifecycle{}, fmt.Errorf("generate ephemeral server token: %w", err)
		}
		lifecycle.Token = generated
		lifecycle.TokenSource = "ephemeral (lifecycle auto-generated)"
	}
	if lifecycle.Enabled {
		if err := integration.ValidateTokenShape(lifecycle.Token); err != nil {
			return serverLifecycle{}, fmt.Errorf("lifecycle OPENPE_SERVER_TOKEN is weak: %w", err)
		}
	}
	if err := validateUnauthenticatedListen(opts.Listen, lifecycle.Token); err != nil {
		return serverLifecycle{}, err
	}
	return lifecycle, nil
}

func configureServerEnhancer(opts serverOptions) (*enhancer.Service, error) {
	provider, err := providers.New(providers.Spec{
		Provider:  opts.Config.Provider,
		MaxTokens: opts.Config.MaxTokens,
		BaseURL:   opts.BaseURL,
		APIKey:    opts.APIKey,
		Model:     opts.Model,
		Timeout:   opts.Timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("configure provider: %w", err)
	}
	service, err := newEnhancerService(provider, opts.Config)
	if err != nil {
		return nil, fmt.Errorf("configure enhancer: %w", err)
	}
	return service, nil
}

func bindAndPublish(opts serverOptions, lifecycle serverLifecycle, stderr io.Writer) (serverBinding, error) {
	// Bind FIRST, publish after: the descriptor is the only discovery channel
	// IDE installers have, so it must never advertise an address that failed
	// to bind (or a ":0" placeholder instead of the kernel-assigned port).
	// CR-002: a second instance used to overwrite the live descriptor, fail
	// ListenAndServe, and delete the survivor's descriptor on exit.
	listener, err := net.Listen("tcp", opts.Listen)
	if err != nil {
		return serverBinding{}, fmt.Errorf("listen on %s: %w", opts.Listen, err)
	}
	binding := serverBinding{
		Listener: listener,
		Address:  listener.Addr().String(),
		cleanup:  func() {},
	}
	if !lifecycle.Enabled {
		return binding, nil
	}
	descriptorPath := opts.Config.Server.DescriptorFile
	if descriptorPath == "" {
		descriptorPath, err = integration.DefaultDescriptorPath()
		if err != nil {
			_ = listener.Close()
			return serverBinding{}, fmt.Errorf("resolve descriptor path: %w", err)
		}
	}
	descriptor := integration.NewLocalServerDescriptor(deriveBaseURL(binding.Address), lifecycle.Token, os.Getpid(), Version)
	if err := integration.WriteDescriptor(descriptorPath, descriptor); err != nil {
		_ = listener.Close()
		return serverBinding{}, fmt.Errorf("write descriptor %s: %w", descriptorPath, err)
	}
	fmt.Fprintf(stderr, "openpe-server: descriptor written to %s (mode 0600)\n", descriptorPath)
	binding.DescriptorPath = descriptorPath
	binding.cleanup = func() {
		// Ownership-aware cleanup: only remove the file if it still names
		// this instance; a sibling that replaced it keeps its lifecycle.
		if removeErr := integration.RemoveDescriptorIfOwned(descriptorPath, os.Getpid(), lifecycle.Token); removeErr != nil {
			fmt.Fprintf(stderr, "openpe-server: cleanup descriptor %s: %v\n", descriptorPath, removeErr)
		}
	}
	return binding, nil
}

func buildHTTPServer(opts serverOptions, lifecycle serverLifecycle, service *enhancer.Service, boundAddr string, stderr io.Writer) *http.Server {
	// Full server-side timeouts: ReadHeaderTimeout alone left slow-body
	// clients holding connections open indefinitely. Writes must outlast the
	// provider call, so WriteTimeout derives from the enhance timeout.
	enhanceTimeout := opts.Timeout
	if enhanceTimeout <= 0 {
		enhanceTimeout = config.DefaultTimeout
	}
	handler := server.NewWithOptions(service, server.Options{
		Token:    lifecycle.Token,
		CORS:     server.CORSOptions{AllowedOrigins: opts.Config.Server.CORSOrigins},
		ErrorLog: stderr,
		Info: server.ServerInfo{
			Version:    Version,
			StartedAt:  time.Now().UTC(),
			ListenAddr: boundAddr,
		},
		DefaultMaxContextTokens: opts.Config.MaxContextTokens,
		PromptTimeout:           enhanceTimeout + 10*time.Second,
	})
	return &http.Server{
		Addr:              boundAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      enhanceTimeout + 30*time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
}

func describeServer(opts serverOptions, lifecycle serverLifecycle, descriptorPath string) serverStatus {
	status := serverStatus{
		Auth:      "disabled (set OPENPE_SERVER_TOKEN to enable bearer auth)",
		CORS:      "disabled",
		Lifecycle: "disabled",
	}
	if lifecycle.Token != "" {
		status.Auth = fmt.Sprintf("enabled via %s", lifecycle.TokenSource)
	}
	if len(opts.Config.Server.CORSOrigins) > 0 {
		status.CORS = fmt.Sprintf("enabled for %s", strings.Join(opts.Config.Server.CORSOrigins, ", "))
	}
	if lifecycle.Enabled {
		status.Lifecycle = fmt.Sprintf("descriptor=%s", descriptorPath)
	}
	return status
}

func serveUntilSignal(httpServer *http.Server, listener net.Listener, status serverStatus, stderr io.Writer) error {
	errCh := make(chan error, 1)
	go func() {
		fmt.Fprintf(stderr,
			"openpe-server: listening on %s (version=%s; auth=%s; cors=%s; lifecycle=%s)\n",
			httpServer.Addr, Version, status.Auth, status.CORS, status.Lifecycle)
		errCh <- httpServer.Serve(listener)
	}()

	signalCh := make(chan os.Signal, 1)
	signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM)
	select {
	case sig := <-signalCh:
		fmt.Fprintf(stderr, "openpe-server: shutting down after %s\n", sig)
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

type configStringValue struct {
	value string
	set   bool
}

func configStringFlag(fs *flag.FlagSet, name string, usage string) *configStringValue {
	value := &configStringValue{}
	fs.Var(value, name, usage)
	return value
}

func (v *configStringValue) String() string {
	if v == nil {
		return ""
	}
	return v.value
}

func (v *configStringValue) Set(value string) error {
	v.value = value
	v.set = true
	return nil
}

func (v *configStringValue) ValueOrDefault(defaultValue string) string {
	if v != nil && v.set {
		return strings.TrimSpace(v.value)
	}
	return strings.TrimSpace(defaultValue)
}
