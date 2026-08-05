package integration

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/AoManoh/openpe/internal/fsatomic"
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
	if err := ValidateTokenShape(strings.TrimSpace(d.Token)); err != nil {
		return fmt.Errorf("descriptor: invalid token: %w", err)
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
// so concurrent readers always see either the previous or the new payload,
// and holds the descriptor's cross-process lock so a sibling's
// ownership-checked cleanup can never interleave with this publish (the
// read-check-remove TOCTOU: A verifies ownership, B replaces the file, A
// removes B's descriptor).
func WriteDescriptor(path string, d LocalServerDescriptor) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("descriptor path is required")
	}
	if err := rejectDescriptorSymlink(path); err != nil {
		return err
	}
	if err := d.Validate(); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create descriptor dir: %w", err)
	}
	unlock, err := fsatomic.Lock(path)
	if err != nil {
		return fmt.Errorf("lock descriptor: %w", err)
	}
	defer unlock()
	payload, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal descriptor: %w", err)
	}
	payload = append(payload, '\n')
	prepare := func(tempPath string) error {
		return restrictDescriptorPermissions(path, tempPath)
	}
	if err := fsatomic.WriteFilePreparedExact(path, payload, 0o600, prepare); err != nil {
		return fmt.Errorf("write descriptor: %w", err)
	}
	if err := validateDescriptorPermissions(path); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("validate published descriptor permissions: %w", err)
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
	if err := rejectDescriptorSymlink(path); err != nil {
		return d, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return d, err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return d, fmt.Errorf("descriptor file %s has insecure mode %#o (want 0600 or stricter)", path, info.Mode().Perm())
	}
	if err := validateDescriptorPermissions(path); err != nil {
		return d, fmt.Errorf("descriptor file %s has insecure permissions: %w", path, err)
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

func rejectDescriptorSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect descriptor path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("descriptor path %s must not be a symlink", path)
	}
	return nil
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

// RemoveDescriptorIfOwned deletes the descriptor only when it still belongs
// to this instance (same PID and token). A second openpe-server that failed
// to bind must not tear down the running instance's descriptor on its way
// out: the file it would delete is the ONLY discovery channel IDE installers
// have. A missing file is fine; a foreign or unreadable file is left in
// place — leaving a stale file behind is recoverable, deleting a live
// sibling's descriptor is not.
//
// The read-verify-remove sequence runs under the descriptor's cross-process
// lock, shared with WriteDescriptor: without it a sibling could republish
// between this function's read and its remove, and the wrong descriptor
// would be deleted despite the ownership check.
func RemoveDescriptorIfOwned(path string, pid int, token string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("descriptor path is required")
	}
	if err := rejectDescriptorSymlink(path); err != nil {
		return err
	}
	unlock, err := fsatomic.Lock(path)
	if err != nil {
		return fmt.Errorf("lock descriptor: %w", err)
	}
	defer unlock()
	d, err := ReadDescriptor(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("descriptor %s left in place (cannot verify ownership): %w", path, err)
	}
	if d.PID != pid || d.Token != strings.TrimSpace(token) {
		// Another instance owns the file now; its lifecycle is not ours to end.
		return nil
	}
	return RemoveDescriptor(path)
}
