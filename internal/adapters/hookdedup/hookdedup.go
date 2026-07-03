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
// it finishes: the next sibling, which only starts after the winner has
// exited, always sees a fresh claim. A deliberate re-submission of the same
// prompt after the freshness window elapses re-enhances as normal.
//
// The claim also records the winner's Outcome (block vs inject), because a
// firing inside the window is not always a sibling of the SAME submission: it
// can be the user resubmitting the same text right after a review block. A
// plain skip is only correct after an injection (the prompt proceeded,
// enhanced); after a block it un-does the interception — the raw `pe` prompt
// sails through to the model (2026-07-03 incident). With the outcome recorded,
// losers replay a block from the delivery cache and keep skipping after an
// injection, which is safe under both interpretations.
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

// Outcome records how a winning hook flight concluded, so a loser inside the
// freshness window can mirror it instead of guessing.
type Outcome string

const (
	// OutcomeUnknown: no conclusion recorded — the winner is still in flight,
	// crashed, or ended in an error. Losers skip, the pre-outcome behaviour.
	OutcomeUnknown Outcome = ""
	// OutcomeBlock: the winner blocked the prompt for review with a cached
	// enhancement. The prompt never reached the model, so a loser may replay
	// the block and re-deliver the cache.
	OutcomeBlock Outcome = "block"
	// OutcomeInject: the winner injected the enhancement and let the prompt
	// proceed. Losers must keep skipping — a replay could double-inject
	// within a single submission event.
	OutcomeInject Outcome = "inject"
)

// Claim attempts to acquire the single-flight claim for prompt within window.
//
// It returns won=true for the first caller of a fresh prompt; the caller must
// do the work and then invoke finish exactly once with the flight's Outcome.
// It returns won=false for callers inside a fresh claim's window, along with
// the winner's recorded Outcome (OutcomeUnknown while the winner is still in
// flight); for a losing caller finish is a no-op.
//
// baseCacheDir mirrors the delivery package's cache-directory resolution so the
// claim directory is shared across every adapter. An empty baseCacheDir falls
// back to OPENPE_CACHE_DIR and then the user cache dir. A non-positive window
// uses DefaultWindow.
//
// Any filesystem error degrades to "won=true, no claim": a transient FS problem
// must never silently drop the user's enhancement, only its de-duplication.
func Claim(baseCacheDir, prompt string, window time.Duration) (won bool, prior Outcome, finish func(Outcome)) {
	if window <= 0 {
		window = DefaultWindow
	}
	noop := func(Outcome) {}
	dir := dedupDir(baseCacheDir)
	if dir == "" {
		return true, OutcomeUnknown, noop
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return true, OutcomeUnknown, noop
	}
	path := filepath.Join(dir, key(prompt))

	// Fast path: atomically create the claim. Success means we are the winner.
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
		_ = f.Close()
		return true, OutcomeUnknown, func(o Outcome) { record(path, o) }
	} else if !os.IsExist(err) {
		// Unexpected FS error: never drop the enhancement.
		return true, OutcomeUnknown, noop
	}

	// A claim already exists. If it is still fresh, a sibling (or an immediate
	// resubmission) owns this prompt: report the winner's outcome and skip.
	if info, err := os.Stat(path); err == nil {
		if time.Since(info.ModTime()) <= window {
			prior := readOutcome(path)
			// Slide the window so a chain of siblings stays deduped, then skip.
			touch(path)
			return false, prior, noop
		}
	}

	// Stale (or unreadable) claim: take it over and become the winner,
	// clearing the previous flight's outcome so this flight's losers do not
	// replay a stale conclusion. The take-over is not perfectly atomic, but it
	// only matters after a crashed winner, and the worst case is a single
	// extra enhancement.
	reset(path)
	return true, OutcomeUnknown, func(o Outcome) { record(path, o) }
}

// record stores the flight's outcome in the claim body and refreshes its
// modification time (sibling hooks that start after the winner exits must
// still observe a fresh claim). Best effort: on write failure it degrades to
// a plain touch so de-duplication itself keeps working.
func record(path string, outcome Outcome) {
	if err := os.WriteFile(path, []byte(outcome), 0o600); err != nil {
		touch(path)
	}
}

// reset truncates a taken-over claim (dropping any stale outcome) and marks
// it fresh. Best effort, like record.
func reset(path string) {
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		touch(path)
	}
}

// readOutcome parses the claim body written by record. Unrecognised or
// unreadable content degrades to OutcomeUnknown (skip — the safe default).
func readOutcome(path string) Outcome {
	data, err := os.ReadFile(path)
	if err != nil {
		return OutcomeUnknown
	}
	switch Outcome(strings.TrimSpace(string(data))) {
	case OutcomeBlock:
		return OutcomeBlock
	case OutcomeInject:
		return OutcomeInject
	default:
		return OutcomeUnknown
	}
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
