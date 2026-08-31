// Package histstatus defines the shared outcome status for session-history
// collectors (codexhistory, claudetranscript, devinhistory).
//
// It exists so the hook layer can, for every client, surface whether prior
// conversation context was actually included in an enhancement — and if not,
// why — instead of silently falling back to a history-less enhancement that the
// user might mistake for a context-aware one. A genuine read failure is still
// reported as a Go error; this status describes the non-error outcomes.
package histstatus

// Status describes the outcome of attempting to collect prior-conversation
// history for one enhancement.
type Status int

const (
	// Unknown is the zero value: the history feature was disabled or not
	// consulted, so no claim is made about context.
	Unknown Status = iota
	// Found: usable prior user/assistant turns were located and included.
	Found
	// NoSession: no session / transcript could be located for the workspace
	// (e.g. a brand-new session with no prior turns, or no store at all).
	NoSession
	// Empty: a session was located but contained no usable user/assistant
	// turns after filtering — or an identified session has no persisted rows
	// yet (a brand-new Devin session's first prompt fires the hook before
	// Devin inserts the session row).
	Empty
	// Stale: a session was located but its last activity is outside the
	// freshness window, so it was deliberately not reused (devin).
	Stale
	// CWDMismatch: the located session belongs to a different workspace than
	// the current one, so its history was not reused.
	CWDMismatch
	// Ambiguous: several sessions are active for this workspace within the
	// freshness window and the current one could not be identified, so no
	// history was attached rather than risk injecting another conversation's
	// context (devin heuristic fallback).
	Ambiguous
)

// IncludedHistory reports whether this status means prior context was actually
// folded into the enhancement.
func (s Status) IncludedHistory() bool { return s == Found }

func (s Status) String() string {
	switch s {
	case Found:
		return "found"
	case NoSession:
		return "no_session"
	case Empty:
		return "empty"
	case Stale:
		return "stale"
	case CWDMismatch:
		return "cwd_mismatch"
	case Ambiguous:
		return "ambiguous"
	default:
		return "unknown"
	}
}
