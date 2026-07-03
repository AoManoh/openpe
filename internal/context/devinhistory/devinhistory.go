// Package devinhistory reads the current Devin CLI session from its local
// SQLite store to populate enhancer.Request.History, giving the Devin hook the
// same context-aware enhancement Codex and Claude Code already have.
//
// Why SQLite (unlike codexhistory/claudetranscript JSONL): Devin persists
// sessions in ~/.local/share/devin/cli/sessions.db (WAL mode). The
// UserPromptSubmit hook carries only the prompt — no session id, and the prompt
// is not yet written to the DB when the hook fires — so the session is located
// in two tiers: preferably by an identified session id (Options.SessionID,
// discovered from the hook's process ancestry by devinsession — exact, so no
// cwd/recency guard applies), otherwise by the working directory +
// most-recent-activity heuristic (bounded by a recency window, and refusing
// with Ambiguous when several sessions in the directory are inside the window
// — guessing there once injected another conversation's history). The
// conversation is reconstructed by walking the session's node forest up
// from sessions.main_chain_id via parent_node_id (node_id is monotonic along a
// chain; created_at is not, so the chain — not a timestamp sort — defines
// order). Compaction needs explicit handling: the post-compaction main chain
// replaces the prior turns with a summary node that Devin writes with role
// "system" (marked summarized_from in the message_nodes.metadata column), so
// the role filter alone would drop it and report a freshly compacted session
// as empty — exactly when the summary is the only carrier of the prior
// conversation (2026-07-03 incident). Summary nodes are therefore kept and
// re-mapped to assistant turns, and Result.SummaryCount reports how many were
// actually delivered.
//
// The store is opened strictly read-only (mode=ro + query_only); this package
// never writes to the live database.
package devinhistory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"

	"github.com/AoManoh/openpe/internal/context/histstatus"
	"github.com/AoManoh/openpe/internal/enhancer"
)

const (
	defaultMaxMessages = 12
	defaultMaxChars    = 12000
	// defaultRecency mirrors config.DefaultDevinHistoryRecency (kept in sync;
	// see its comment for the 2h -> 6h rationale). Used only when a caller
	// passes Options.Recency <= 0.
	defaultRecency = 6 * time.Hour
	queryTimeout   = 5 * time.Second
)

type Options struct {
	DBPath      string
	MaxMessages int
	MaxChars    int
	Recency     time.Duration
	// SessionID, when non-empty, is the identified session (e.g. discovered by
	// devinsession from the hook's process ancestry): the session is loaded by
	// id, with no cwd or recency filtering — identity makes both guards
	// redundant, so resuming yesterday's conversation still gets its history.
	// When empty, or when the id matches no session row, Retrieve falls back
	// to the cwd+recency heuristic.
	SessionID string
}

type Result struct {
	SessionID string
	Status    histstatus.Status
	Messages  []enhancer.Message
	// SummaryCount is how many of Messages are Devin compaction summaries
	// (system-role nodes marked summarized_from, re-mapped to assistant so
	// every prompt style keeps them). It counts what was actually delivered
	// after limiting — never a summary that MaxMessages/MaxChars evicted — so
	// the caller's disclosure can truthfully say a summary was included.
	SummaryCount int
}

type Collector struct {
	dbPath      string
	maxMessages int
	maxChars    int
	recency     time.Duration
	sessionID   string
}

func New(opts Options) *Collector {
	dbPath := strings.TrimSpace(opts.DBPath)
	if dbPath == "" {
		dbPath = defaultDBPath()
	}
	maxMessages := opts.MaxMessages
	if maxMessages <= 0 {
		maxMessages = defaultMaxMessages
	}
	maxChars := opts.MaxChars
	if maxChars <= 0 {
		maxChars = defaultMaxChars
	}
	recency := opts.Recency
	if recency <= 0 {
		recency = defaultRecency
	}
	return &Collector{
		dbPath:      dbPath,
		maxMessages: maxMessages,
		maxChars:    maxChars,
		recency:     recency,
		sessionID:   strings.TrimSpace(opts.SessionID),
	}
}

// Retrieve locates the Devin session for cwd and returns its recent
// user/assistant history. The returned error is non-nil only on a genuine read
// failure (DB present but unreadable / unparseable); "no session", "stale" and
// "empty" are reported via Result.Status so the caller can surface them
// explicitly instead of silently enhancing without context. prompt is used only
// to drop a trailing history turn identical to the current prompt.
func (c *Collector) Retrieve(prompt string, cwd string) (Result, error) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return Result{Status: histstatus.NoSession}, nil
	}
	if _, err := os.Stat(c.dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No Devin store on this machine: explicit "no history", not a failure.
			return Result{Status: histstatus.NoSession}, nil
		}
		return Result{}, fmt.Errorf("stat devin sessions db: %w", err)
	}

	db, err := openReadOnly(c.dbPath)
	if err != nil {
		return Result{}, fmt.Errorf("open devin sessions db: %w", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	// Identified path first: a session id discovered from the hook's process
	// ancestry is authoritative — no cwd or recency guard needed (both exist
	// only because the heuristic cannot be sure it found the right session).
	// An id that matches no row (e.g. stale lock parse) falls back.
	sess, status := sessionRow{}, histstatus.NoSession
	if c.sessionID != "" {
		sess, status, err = locateSessionByID(ctx, db, c.sessionID)
		if err != nil {
			return Result{}, err
		}
	}
	if status != histstatus.Found {
		sess, status, err = locateSession(ctx, db, cwd, c.recency)
		if err != nil {
			return Result{}, err
		}
	}
	if status != histstatus.Found {
		return Result{SessionID: sess.id, Status: status}, nil
	}

	chain, err := loadChainMessages(ctx, db, sess.id, sess.mainChainID)
	if err != nil {
		return Result{SessionID: sess.id}, err
	}
	chain = dropTrailingPrompt(chain, prompt)
	chain = limitMessages(chain, c.maxMessages, c.maxChars)
	if len(chain) == 0 {
		return Result{SessionID: sess.id, Status: histstatus.Empty}, nil
	}
	messages := make([]enhancer.Message, len(chain))
	summaries := 0
	for i, m := range chain {
		messages[i] = enhancer.Message{Role: m.Role, Content: m.Content}
		if m.Summary {
			summaries++
		}
	}
	return Result{SessionID: sess.id, Status: histstatus.Found, Messages: messages, SummaryCount: summaries}, nil
}

// chainMessage is the collector-internal message shape: an enhancer.Message
// plus whether the node is a compaction summary. The flag rides through
// dropTrailingPrompt/limitMessages so Result.SummaryCount reflects what is
// actually kept, not what the chain contained.
type chainMessage struct {
	Role    string
	Content string
	Summary bool
}

type sessionRow struct {
	id          string
	mainChainID sql.NullInt64
}

// locateSessionByID loads an identified session directly. A missing row is
// reported as NoSession so the caller can fall back to the heuristic.
func locateSessionByID(ctx context.Context, db *sql.DB, sessionID string) (sessionRow, histstatus.Status, error) {
	const q = `SELECT id, main_chain_id FROM sessions WHERE id = ? AND hidden = 0`
	var s sessionRow
	err := db.QueryRowContext(ctx, q, sessionID).Scan(&s.id, &s.mainChainID)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionRow{}, histstatus.NoSession, nil
	}
	if err != nil {
		return sessionRow{}, histstatus.Unknown, fmt.Errorf("query devin session by id: %w", err)
	}
	return s, histstatus.Found, nil
}

// locateSession is the cwd+recency heuristic fallback (used when no identified
// session id is available — non-Linux, or ancestry discovery failed). When
// SEVERAL sessions in this directory are inside the recency window, the
// current one cannot be told apart, so it reports Ambiguous instead of
// guessing: injecting another conversation's history is strictly worse than
// enhancing without history (a real cross-session incident motivated this).
func locateSession(ctx context.Context, db *sql.DB, cwd string, recency time.Duration) (sessionRow, histstatus.Status, error) {
	const q = `SELECT id, main_chain_id, last_activity_at
		FROM sessions
		WHERE working_directory = ? AND hidden = 0
		ORDER BY last_activity_at DESC
		LIMIT 2`
	rows, err := db.QueryContext(ctx, q, cwd)
	if err != nil {
		return sessionRow{}, histstatus.Unknown, fmt.Errorf("query devin session: %w", err)
	}
	defer rows.Close()

	type candidate struct {
		row          sessionRow
		lastActivity sql.NullInt64
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.row.id, &c.row.mainChainID, &c.lastActivity); err != nil {
			return sessionRow{}, histstatus.Unknown, fmt.Errorf("scan devin session: %w", err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return sessionRow{}, histstatus.Unknown, fmt.Errorf("iterate devin sessions: %w", err)
	}
	if len(candidates) == 0 {
		return sessionRow{}, histstatus.NoSession, nil
	}
	fresh := func(c candidate) bool {
		return !c.lastActivity.Valid || time.Since(time.Unix(c.lastActivity.Int64, 0)) <= recency
	}
	if len(candidates) > 1 && fresh(candidates[1]) {
		// Two or more live sessions in this directory: ambiguous.
		return sessionRow{}, histstatus.Ambiguous, nil
	}
	if !fresh(candidates[0]) {
		return candidates[0].row, histstatus.Stale, nil
	}
	return candidates[0].row, histstatus.Found, nil
}

func loadChainMessages(ctx context.Context, db *sql.DB, sessionID string, mainChainID sql.NullInt64) ([]chainMessage, error) {
	if !mainChainID.Valid {
		return nil, nil
	}
	const q = `SELECT node_id, parent_node_id, chat_message, metadata FROM message_nodes WHERE session_id = ?`
	rows, err := db.QueryContext(ctx, q, sessionID)
	if err != nil {
		// Older Devin CLI schemas may predate the metadata column; retry
		// without it. Summaries then cannot be identified, which degrades to
		// the pre-summary behaviour instead of failing the whole history read.
		const legacy = `SELECT node_id, parent_node_id, chat_message, NULL FROM message_nodes WHERE session_id = ?`
		rows, err = db.QueryContext(ctx, legacy, sessionID)
		if err != nil {
			return nil, fmt.Errorf("query devin message_nodes: %w", err)
		}
	}
	defer rows.Close()

	type node struct {
		parent  sql.NullInt64
		chat    string
		summary bool
	}
	nodes := make(map[int64]node)
	for rows.Next() {
		var id int64
		var parent sql.NullInt64
		var chat string
		var meta sql.NullString
		if err := rows.Scan(&id, &parent, &chat, &meta); err != nil {
			return nil, fmt.Errorf("scan devin message_node: %w", err)
		}
		nodes[id] = node{parent: parent, chat: chat, summary: summaryMarked(meta)}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate devin message_nodes: %w", err)
	}

	// Walk up the main chain (leaf -> root) via parent_node_id, then reverse to
	// chronological order. node_id is monotonic along a chain, so the cycle
	// guard is belt-and-suspenders against a malformed forest.
	var chainIDs []int64
	seen := make(map[int64]bool)
	for cur := mainChainID; cur.Valid; {
		id := cur.Int64
		if seen[id] {
			break
		}
		seen[id] = true
		n, ok := nodes[id]
		if !ok {
			break
		}
		chainIDs = append(chainIDs, id)
		cur = n.parent
	}

	messages := make([]chainMessage, 0, len(chainIDs))
	for i := len(chainIDs) - 1; i >= 0; i-- {
		n := nodes[chainIDs[i]]
		msg, ok := parseChatMessage(n.chat, n.summary)
		if !ok {
			continue
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

// summaryMarked reports whether a message_nodes.metadata column value marks a
// compaction summary. Devin writes {"summarized_from": <old chain leaf>} on
// summary nodes; system-prefix nodes carry the KEY with a null value, so the
// marker is a non-null value, not key presence.
func summaryMarked(meta sql.NullString) bool {
	if !meta.Valid || strings.TrimSpace(meta.String) == "" {
		return false
	}
	var m struct {
		SummarizedFrom *int64 `json:"summarized_from"`
	}
	if err := json.Unmarshal([]byte(meta.String), &m); err != nil {
		return false
	}
	return m.SummarizedFrom != nil
}

type chatMessageEnvelope struct {
	Role     string          `json:"role"`
	Content  json.RawMessage `json:"content"`
	Metadata struct {
		IsUserInput *bool `json:"is_user_input"`
	} `json:"metadata"`
}

// parseChatMessage keeps genuine assistant and user turns and drops system /
// tool nodes — with one exception: a system node marked as a compaction
// summary (summaryNode) is kept and re-mapped to an assistant turn, because
// right after a compaction it is the only carrier of the prior conversation
// and prompt styles drop non-user/assistant turns (2026-07-03 incident:
// filtering it made every post-compaction enhancement history-less). A user
// node is only kept when metadata.is_user_input is true: Devin also stores
// injected user-role context (rules, system_info, hook additionalContext)
// which would be circular noise in the rewriter's context.
func parseChatMessage(raw string, summaryNode bool) (chainMessage, bool) {
	var env chatMessageEnvelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return chainMessage{}, false
	}
	role := strings.TrimSpace(env.Role)
	summary := false
	switch role {
	case "assistant":
		// keep; an in-place compaction may append the summary as an assistant
		// turn, which counts as a summary without needing a role re-map.
		summary = summaryNode
	case "user":
		if env.Metadata.IsUserInput == nil || !*env.Metadata.IsUserInput {
			return chainMessage{}, false
		}
	case "system":
		if !summaryNode {
			return chainMessage{}, false
		}
		role = "assistant"
		summary = true
	default:
		return chainMessage{}, false
	}
	content := extractContent(env.Content)
	if content == "" {
		return chainMessage{}, false
	}
	return chainMessage{Role: role, Content: content, Summary: summary}, true
}

func extractContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return strings.TrimSpace(value)
	}
	var parts []map[string]any
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, part := range parts {
		if text, ok := part["text"].(string); ok {
			b.WriteString(text)
		}
	}
	return strings.TrimSpace(b.String())
}

// dropTrailingPrompt removes a final user turn identical to the current prompt.
// Devin does not persist the in-flight prompt before the hook fires, so this is
// defensive against any timing where the current prompt already appears as the
// last turn (avoids asking the model to "rewrite" against itself).
func dropTrailingPrompt(messages []chainMessage, prompt string) []chainMessage {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" || len(messages) == 0 {
		return messages
	}
	last := messages[len(messages)-1]
	if strings.TrimSpace(last.Role) == "user" && strings.TrimSpace(last.Content) == prompt {
		return messages[:len(messages)-1]
	}
	return messages
}

func limitMessages(messages []chainMessage, maxMessages int, maxChars int) []chainMessage {
	if maxMessages <= 0 {
		maxMessages = defaultMaxMessages
	}
	if maxChars <= 0 {
		maxChars = defaultMaxChars
	}
	var reversed []chainMessage
	remaining := maxChars
	for i := len(messages) - 1; i >= 0 && len(reversed) < maxMessages && remaining > 0; i-- {
		content := strings.TrimSpace(messages[i].Content)
		if content == "" {
			continue
		}
		if runeLen(content) > remaining {
			content = truncateRunes(content, remaining)
		}
		reversed = append(reversed, chainMessage{Role: messages[i].Role, Content: content, Summary: messages[i].Summary})
		remaining -= runeLen(content)
	}
	out := make([]chainMessage, len(reversed))
	for i := range reversed {
		out[len(reversed)-1-i] = reversed[i]
	}
	return out
}

func openReadOnly(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	// mode=ro: never create or write the live DB. query_only: reject writes at
	// the SQL layer too. busy_timeout: wait briefly rather than failing if a
	// writer holds a lock (WAL allows concurrent readers, so this is rare).
	dsn := "file:" + abs + "?mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(true)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// DefaultDBPath exposes the platform-default sessions.db location so callers
// (e.g. the hook's devinsession lock-dir derivation) resolve paths exactly the
// way this collector does when Options.DBPath is empty.
func DefaultDBPath() string { return defaultDBPath() }

func defaultDBPath() string {
	if runtime.GOOS == "windows" {
		if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
			return filepath.Join(appData, "devin", "cli", "sessions.db")
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "share", "devin", "cli", "sessions.db")
	}
	return filepath.Join(".local", "share", "devin", "cli", "sessions.db")
}

func runeLen(value string) int {
	return utf8.RuneCountInString(value)
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if runeLen(value) <= limit {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
