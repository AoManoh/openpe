package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/AoManoh/openpe/internal/config"
	"github.com/AoManoh/openpe/internal/enhancer"
	"github.com/AoManoh/openpe/internal/integration"
	"github.com/AoManoh/openpe/internal/wiring"
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
	return runWithIO(args, os.Stdout, os.Stderr)
}

func runWithIO(args []string, stdout io.Writer, stderr io.Writer) error {
	opts, shouldRun, err := parseServerOptions(args, stdout)
	if err != nil || !shouldRun {
		return err
	}
	lifecycle, err := prepareLifecycle(opts)
	if err != nil {
		return err
	}
	service, err := configureServerEnhancer(opts)
	if err != nil {
		return err
	}
	binding, err := bindAndPublish(opts, lifecycle, stderr)
	if err != nil {
		return err
	}
	defer binding.Listener.Close()
	defer binding.cleanup()
	httpServer := buildHTTPServer(opts, lifecycle, service, binding.Address, stderr)
	status := describeServer(opts, lifecycle, binding.DescriptorPath)
	return serveUntilSignal(httpServer, binding.Listener, status, stderr)
}

func validateUnauthenticatedListen(listenAddr string, token string) error {
	host, _, err := net.SplitHostPort(strings.TrimSpace(listenAddr))
	if err != nil {
		return fmt.Errorf("validate listen address %q: %w", listenAddr, err)
	}
	if isAllowedUnauthenticatedHost(host) {
		return nil
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("refusing unauthenticated listen address %q: set OPENPE_SERVER_TOKEN or bind to 127.0.0.1, ::1, or localhost", listenAddr)
	}
	// A network-reachable server needs authentication material that cannot be
	// guessed. "Any non-empty string counts" once allowed OPENPE_SERVER_TOKEN=x
	// to pass this gate; enforce the same 256-bit hex shape GenerateToken
	// produces (integration.ValidateTokenShape) before exposing the enhancer
	// (and the provider budget behind it) beyond loopback.
	if err := integration.ValidateTokenShape(token); err != nil {
		return fmt.Errorf(
			"refusing non-loopback listen address %q with a weak OPENPE_SERVER_TOKEN (%v); generate one with: openssl rand -hex 32",
			listenAddr, err,
		)
	}
	return nil
}

func isAllowedUnauthenticatedHost(host string) bool {
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if zoneIndex := strings.LastIndex(host, "%"); zoneIndex >= 0 {
		host = host[:zoneIndex]
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.Equal(net.IPv4(127, 0, 0, 1)) || ip.Equal(net.IPv6loopback))
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

// newEnhancerService delegates to the shared wiring builder. The previous
// hand-maintained copy had drifted from the CLI's (CR-009: it never applied
// OPENPE_MESSAGE_STYLE, so HTTP requests always used the flatten layout).
func newEnhancerService(provider enhancer.Provider, cfg config.Config) (*enhancer.Service, error) {
	return wiring.NewEnhancerService(provider, cfg)
}
