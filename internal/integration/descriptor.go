package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LocalServerDescriptor is the handshake payload openPE's local HTTP server
// exposes to IDE installers running on the same host.
//
// The descriptor is normally persisted to ~/.config/openpe/server.json with
// file mode 0600. IDE installers read this file (or call GET /v1/info while
// authenticated) to learn the base URL and bearer token.
type LocalServerDescriptor struct {
	// BaseURL is the loopback HTTP endpoint, e.g. http://127.0.0.1:18980.
	BaseURL string `json:"base_url"`
	// Token is the bearer token clients must send as Authorization header.
	Token string `json:"token"`
	// PID is the openpe-server process identifier; installers can check
	// liveness with os.FindProcess + signal 0.
	PID int `json:"pid"`
	// StartedAt is the server start time in RFC3339 format.
	StartedAt string `json:"started_at"`
	// Version is the openpe-server build version string, empty when unset.
	Version string `json:"version,omitempty"`
}

// NewLocalServerDescriptor constructs a descriptor with the supplied fields
// and a StartedAt timestamp set to time.Now().UTC().
func NewLocalServerDescriptor(baseURL, token string, pid int, version string) LocalServerDescriptor {
	return LocalServerDescriptor{
		BaseURL:   strings.TrimSpace(baseURL),
		Token:     strings.TrimSpace(token),
		PID:       pid,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		Version:   strings.TrimSpace(version),
	}
}

// Validate ensures all required fields are present and well-formed.
func (d LocalServerDescriptor) Validate() error {
	if strings.TrimSpace(d.BaseURL) == "" {
		return errors.New("descriptor: base_url is required")
	}
	if strings.TrimSpace(d.Token) == "" {
		return errors.New("descriptor: token is required")
	}
	if d.PID <= 0 {
		return errors.New("descriptor: pid must be positive")
	}
	return nil
}

// DefaultDescriptorPath returns the canonical descriptor file location.
// Resolution order:
//
//  1. OPENPE_SERVER_DESCRIPTOR_FILE — explicit override.
//  2. XDG_CONFIG_HOME/openpe/server.json — XDG-aware desktops.
//  3. <user home>/.config/openpe/server.json — fallback.
func DefaultDescriptorPath() (string, error) {
	if value := strings.TrimSpace(os.Getenv("OPENPE_SERVER_DESCRIPTOR_FILE")); value != "" {
		return value, nil
	}
	base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "openpe", "server.json"), nil
}

// WriteDescriptor atomically persists d to path with mode 0600. Parent
// directories are created with mode 0700. The write uses a temp file + rename
// so concurrent readers always see either the previous or the new payload.
func WriteDescriptor(path string, d LocalServerDescriptor) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("descriptor path is required")
	}
	if err := d.Validate(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create descriptor dir: %w", err)
	}
	payload, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal descriptor: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".server-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp descriptor: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp descriptor: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod temp descriptor: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp descriptor: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("rename descriptor: %w", err)
	}
	return nil
}

// ReadDescriptor loads a descriptor previously written by WriteDescriptor.
// It refuses to read files whose mode is broader than 0600 because they may
// have leaked the bearer token to other local users.
func ReadDescriptor(path string) (LocalServerDescriptor, error) {
	var d LocalServerDescriptor
	if strings.TrimSpace(path) == "" {
		return d, errors.New("descriptor path is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return d, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return d, fmt.Errorf("descriptor file %s has insecure mode %#o (want 0600 or stricter)", path, info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, fmt.Errorf("parse descriptor: %w", err)
	}
	if err := d.Validate(); err != nil {
		return d, err
	}
	return d, nil
}

// RemoveDescriptor deletes the descriptor file if it exists. A missing file
// is not treated as an error.
func RemoveDescriptor(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("descriptor path is required")
	}
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
