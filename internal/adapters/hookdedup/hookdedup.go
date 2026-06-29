// Package hookdedup provides cross-adapter single-flight de-duplication for
// openPE hook runs.
//
// Some host agents aggregate the UserPromptSubmit event from multiple
// ecosystems. The Devin CLI in particular loads hooks from its own config,
// from Claude Code (~/.claude/*), and from Windsurf (~/.codeium/windsurf/
// hooks.json) all at once, then fires every matching hook for a single user
// prompt. With openPE installed for more than one client this means the same
// prompt is enhanced two or three times — wasting latency and provider cost
// and producing conflicting hook output.
//
// Claim implements a filesystem-backed single-flight lock keyed on the prompt
// text: the first hook to fire for a given prompt wins and performs the
// enhancement; sibling hooks for the same prompt observe the fresh claim and
// skip. Hosts run the sibling hooks sequentially (each waits for the previous
// process to exit), so a winner refreshes the claim's modification time when
// it finishes (Done): the next sibling, which only starts after the winner has
// exited, always sees a fresh claim. A deliberate re-submission of the same
// prompt after the freshness window elapses re-enhances as normal.
package hookdedup

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultWindow is the claim freshness window. It must comfortably exceed the
// gap between a winner hook exiting and the next sibling hook starting (host
// dispatch latency, on the order of milliseconds) while staying short enough
// that a deliberate re-submission of the same prompt re-enhances promptly.
const DefaultWindow = 5 * time.Second

// Claim attempts to acquire the single-flight claim for prompt within window.
//
// It returns won=true for the first caller of a fresh prompt (the caller must
// do the work and then invoke done exactly once), and won=false for sibling
// callers that should skip the enhancement. done is always non-nil; for a
// losing caller it is a no-op.
//
// baseCacheDir mirrors the delivery package's cache-directory resolution so the
// claim directory is shared across every adapter. An empty baseCacheDir falls
// back to OPENPE_CACHE_DIR and then the user cache dir. A non-positive window
// uses DefaultWindow.
//
// Any filesystem error degrades to "won=true, no claim": a transient FS problem
// must never silently drop the user's enhancement, only its de-duplication.
func Claim(baseCacheDir, prompt string, window time.Duration) (won bool, done func()) {
	if window <= 0 {
		window = DefaultWindow
	}
	dir := dedupDir(baseCacheDir)
	if dir == "" {
		return true, func() {}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return true, func() {}
	}
	path := filepath.Join(dir, key(prompt))

	// Fast path: atomically create the claim. Success means we are the winner.
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
		_ = f.Close()
		return true, func() { touch(path) }
	} else if !os.IsExist(err) {
		// Unexpected FS error: never drop the enhancement.
		return true, func() {}
	}

	// A claim already exists. If it is still fresh, a sibling owns this prompt.
	if info, err := os.Stat(path); err == nil {
		if time.Since(info.ModTime()) <= window {
			// Slide the window so a chain of siblings stays deduped, then skip.
			touch(path)
			return false, func() {}
		}
	}

	// Stale (or unreadable) claim: take it over and become the winner. The
	// take-over is not perfectly atomic, but it only matters after a crashed
	// winner, and the worst case is a single extra enhancement.
	touch(path)
	return true, func() { touch(path) }
}

// touch refreshes the claim's modification time to now, recreating the file if
// it has been removed in the meantime. Best effort by design.
func touch(path string) {
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		if f, e := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600); e == nil {
			_ = f.Close()
		}
	}
}

func key(prompt string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(prompt)))
	return hex.EncodeToString(sum[:])
}

// dedupDir resolves the shared claim directory. It deliberately mirrors
// delivery.cacheDir's roots (override, OPENPE_CACHE_DIR, user cache dir) but
// uses a single fixed ".dedup" leaf so every client coordinates in the same
// place regardless of its per-client cache namespace.
func dedupDir(baseCacheDir string) string {
	if v := strings.TrimSpace(baseCacheDir); v != "" {
		return filepath.Join(v, ".dedup")
	}
	if v := strings.TrimSpace(os.Getenv("OPENPE_CACHE_DIR")); v != "" {
		return filepath.Join(v, ".dedup")
	}
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "openpe", ".dedup")
	}
	return ""
}
