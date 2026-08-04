// Package hookdedup provides cross-adapter single-flight de-duplication for
// openPE hook runs.
//
// Some host agents aggregate the UserPromptSubmit event from multiple
// ecosystems. The Devin CLI in particular loads hooks from its own config AND
// from Claude Code (~/.claude/*) at once (2026-07 probe of 2026.8.18: the
// Windsurf hooks.json is NOT loaded, contrary to an earlier assumption), then
// fires EVERY loaded hook for a single user prompt — a block does not
// short-circuit the rest, and which hook's block reason the host displays
// depends on source/order. With openPE installed for more than one format
// this means the same prompt would be enhanced multiple times — wasting
// latency and provider cost and producing conflicting hook output.
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
// losers replay a block from the claim body itself (which carries the
// winner's enhanced prompt — the global per-client cache may already belong
// to a parallel session's flight, CR-003) and keep skipping after an
// injection, which is safe under both interpretations.
package hookdedup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// Prior is what a losing caller learns about the winning flight: how it
// concluded, (for blocks) the winner's disclosure notes so a replayed block
// can show the user the same information the winner produced, and the
// winner's enhanced prompt itself. Hosts like the Devin CLI run EVERY hook
// and display the LAST block reason, so a loser's output is often the one
// the user actually sees.
//
// Prompt lives IN the claim (not in the per-client "last prompt" cache) so a
// replay is bound to this exact claim key: with parallel sessions the global
// last-prompt file may already belong to ANOTHER session's flight by the time
// a loser replays (CR-003), which once leaked one workspace's enhancement
// into another. A loser must render from Prior.Prompt or degrade explicitly.
type Prior struct {
	Outcome Outcome
	Notes   string
	Prompt  string
}

// claimBody is the persisted claim conclusion. JSON gives the three fields an
// unambiguous encoding (notes may contain newlines; prompts may contain
// anything); an unparsable or legacy body degrades to OutcomeUnknown, which
// losers already treat as "skip", the safe default.
type claimBody struct {
	Outcome string `json:"outcome"`
	Notes   string `json:"notes,omitempty"`
	Prompt  string `json:"prompt,omitempty"`
}

// Claim attempts to acquire the single-flight claim for prompt within window.
//
// It returns won=true for the first caller of a fresh prompt; the caller must
// do the work and then invoke finish exactly once with the flight's Outcome
// and (for blocks) its disclosure notes. It returns won=false for callers
// inside a fresh claim's window, along with the winner's recorded Prior
// (OutcomeUnknown while the winner is still in flight); for a losing caller
// finish is a no-op.
//
// baseCacheDir mirrors the delivery package's cache-directory resolution so the
// claim directory is shared across every adapter. An empty baseCacheDir falls
// back to OPENPE_CACHE_DIR and then the user cache dir. A non-positive window
// uses DefaultWindow.
//
// Any filesystem error degrades to "won=true, no claim": a transient FS problem
// must never silently drop the user's enhancement, only its de-duplication.
func Claim(baseCacheDir, prompt string, window time.Duration) (won bool, prior Prior, finish func(Outcome, string, string)) {
	if window <= 0 {
		window = DefaultWindow
	}
	noop := func(Outcome, string, string) {}
	dir := dedupDir(baseCacheDir)
	if dir == "" {
		return true, Prior{}, noop
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return true, Prior{}, noop
	}
	path := filepath.Join(dir, key(prompt))

	// Fast path: atomically create the claim. Success means we are the winner.
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
		_ = f.Close()
		return true, Prior{}, func(o Outcome, notes string, enhanced string) { record(path, o, notes, enhanced) }
	} else if !os.IsExist(err) {
		// Unexpected FS error: never drop the enhancement.
		return true, Prior{}, noop
	}

	// A claim already exists. If it is still fresh, a sibling (or an immediate
	// resubmission) owns this prompt: report the winner's conclusion and skip.
	if info, err := os.Stat(path); err == nil {
		if time.Since(info.ModTime()) <= window {
			prior := readPrior(path)
			// Slide the window so a chain of siblings stays deduped, then skip.
			touch(path)
			return false, prior, noop
		}
	}

	// Stale (or unreadable) claim: take it over and become the winner,
	// clearing the previous flight's conclusion so this flight's losers do
	// not replay it. The take-over is not perfectly atomic, but it only
	// matters after a crashed winner, and the worst case is a single extra
	// enhancement.
	reset(path)
	return true, Prior{}, func(o Outcome, notes string, enhanced string) { record(path, o, notes, enhanced) }
}

// record stores the flight's conclusion (outcome, disclosure notes, and for
// blocks the enhanced prompt itself) as JSON in the claim body and refreshes
// its modification time (sibling hooks that start after the winner exits must
// still observe a fresh claim). Best effort: on failure it degrades to a
// plain touch so de-duplication itself keeps working.
func record(path string, outcome Outcome, notes string, enhanced string) {
	payload, err := json.Marshal(claimBody{Outcome: string(outcome), Notes: notes, Prompt: enhanced})
	if err != nil {
		touch(path)
		return
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		touch(path)
	}
}

// reset truncates a taken-over claim (dropping any stale conclusion) and
// marks it fresh. Best effort, like record.
func reset(path string) {
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		touch(path)
	}
}

// readPrior parses the claim body written by record. Unrecognised or
// unreadable content (including bodies written by older binaries) degrades to
// OutcomeUnknown with no notes (skip — the safe default); notes and prompt
// are only meaningful for a recognised outcome.
func readPrior(path string) Prior {
	data, err := os.ReadFile(path)
	if err != nil {
		return Prior{}
	}
	var body claimBody
	if err := json.Unmarshal(data, &body); err != nil {
		return Prior{}
	}
	switch Outcome(strings.TrimSpace(body.Outcome)) {
	case OutcomeBlock:
		return Prior{Outcome: OutcomeBlock, Notes: body.Notes, Prompt: body.Prompt}
	case OutcomeInject:
		return Prior{Outcome: OutcomeInject, Notes: body.Notes, Prompt: body.Prompt}
	default:
		return Prior{}
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
