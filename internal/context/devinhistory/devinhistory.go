// Package devinhistory reads the current Devin CLI session from its local
// SQLite store to populate enhancer.Request.History, giving the Devin hook the
// same context-aware enhancement Codex and Claude Code already have.
//
// Why SQLite (unlike codexhistory/claudetranscript JSONL): Devin persists
// sessions in ~/.local/share/devin/cli/sessions.db (WAL mode). The
// UserPromptSubmit hook carries only the prompt — no session id, and the prompt
// is not yet written to the DB when the hook fires — so the session is located
// by working directory + most-recent activity (bounded by a recency window),
// and the conversation is reconstructed by walking the session's node forest up
// from sessions.main_chain_id via parent_node_id (node_id is monotonic along a
// chain; created_at is not, so the chain — not a timestamp sort — defines
// order). Compaction is handled for free: the main chain runs through Devin's
// summary nodes, not the replaced pre-compaction detail.
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
	defaultRecency     = 2 * time.Hour
	queryTimeout       = 5 * time.Second
)

type Options struct {
	DBPath      string
	MaxMessages int
	MaxChars    int
	Recency     time.Duration
}

type Result struct {
	SessionID string
	Status    histstatus.Status
	Messages  []enhancer.Message
}

type Collector struct {
	dbPath      string
	maxMessages int
	maxChars    int
	recency     time.Duration
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
	return &Collector{dbPath: dbPath, maxMessages: maxMessages, maxChars: maxChars, recency: recency}
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

	sess, status, err := locateSession(ctx, db, cwd, c.recency)
	if err != nil {
		return Result{}, err
	}
	if status != histstatus.Found {
		return Result{SessionID: sess.id, Status: status}, nil
	}

	messages, err := loadChainMessages(ctx, db, sess.id, sess.mainChainID)
	if err != nil {
		return Result{SessionID: sess.id}, err
	}
	messages = dropTrailingPrompt(messages, prompt)
	messages = limitMessages(messages, c.maxMessages, c.maxChars)
	if len(messages) == 0 {
		return Result{SessionID: sess.id, Status: histstatus.Empty}, nil
	}
	return Result{SessionID: sess.id, Status: histstatus.Found, Messages: messages}, nil
}

type sessionRow struct {
	id          string
	mainChainID sql.NullInt64
}

func locateSession(ctx context.Context, db *sql.DB, cwd string, recency time.Duration) (sessionRow, histstatus.Status, error) {
	const q = `SELECT id, main_chain_id, last_activity_at
		FROM sessions
		WHERE working_directory = ? AND hidden = 0
		ORDER BY last_activity_at DESC
		LIMIT 1`
	var s sessionRow
	var lastActivity sql.NullInt64
	err := db.QueryRowContext(ctx, q, cwd).Scan(&s.id, &s.mainChainID, &lastActivity)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionRow{}, histstatus.NoSession, nil
	}
	if err != nil {
		return sessionRow{}, histstatus.Unknown, fmt.Errorf("query devin session: %w", err)
	}
	if lastActivity.Valid && time.Since(time.Unix(lastActivity.Int64, 0)) > recency {
		return s, histstatus.Stale, nil
	}
	return s, histstatus.Found, nil
}

func loadChainMessages(ctx context.Context, db *sql.DB, sessionID string, mainChainID sql.NullInt64) ([]enhancer.Message, error) {
	if !mainChainID.Valid {
		return nil, nil
	}
	const q = `SELECT node_id, parent_node_id, chat_message FROM message_nodes WHERE session_id = ?`
	rows, err := db.QueryContext(ctx, q, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query devin message_nodes: %w", err)
	}
	defer rows.Close()

	type node struct {
		parent sql.NullInt64
		chat   string
	}
	nodes := make(map[int64]node)
	for rows.Next() {
		var id int64
		var parent sql.NullInt64
		var chat string
		if err := rows.Scan(&id, &parent, &chat); err != nil {
			return nil, fmt.Errorf("scan devin message_node: %w", err)
		}
		nodes[id] = node{parent: parent, chat: chat}
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

	messages := make([]enhancer.Message, 0, len(chainIDs))
	for i := len(chainIDs) - 1; i >= 0; i-- {
		role, content, ok := parseChatMessage(nodes[chainIDs[i]].chat)
		if !ok {
			continue
		}
		messages = append(messages, enhancer.Message{Role: role, Content: content})
	}
	return messages, nil
}

type chatMessageEnvelope struct {
	Role     string          `json:"role"`
	Content  json.RawMessage `json:"content"`
	Metadata struct {
		IsUserInput *bool `json:"is_user_input"`
	} `json:"metadata"`
}

// parseChatMessage keeps genuine assistant and user turns and drops system /
// tool nodes. A user node is only kept when metadata.is_user_input is true:
// Devin also stores injected user-role context (rules, system_info, hook
// additionalContext) which would be circular noise in the rewriter's context.
func parseChatMessage(raw string) (string, string, bool) {
	var env chatMessageEnvelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return "", "", false
	}
	role := strings.TrimSpace(env.Role)
	switch role {
	case "assistant":
		// keep
	case "user":
		if env.Metadata.IsUserInput == nil || !*env.Metadata.IsUserInput {
			return "", "", false
		}
	default:
		return "", "", false
	}
	content := extractContent(env.Content)
	if content == "" {
		return "", "", false
	}
	return role, content, true
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
func dropTrailingPrompt(messages []enhancer.Message, prompt string) []enhancer.Message {
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

func limitMessages(messages []enhancer.Message, maxMessages int, maxChars int) []enhancer.Message {
	if maxMessages <= 0 {
		maxMessages = defaultMaxMessages
	}
	if maxChars <= 0 {
		maxChars = defaultMaxChars
	}
	var reversed []enhancer.Message
	remaining := maxChars
	for i := len(messages) - 1; i >= 0 && len(reversed) < maxMessages && remaining > 0; i-- {
		content := strings.TrimSpace(messages[i].Content)
		if content == "" {
			continue
		}
		if runeLen(content) > remaining {
			content = truncateRunes(content, remaining)
		}
		reversed = append(reversed, enhancer.Message{Role: messages[i].Role, Content: content})
		remaining -= runeLen(content)
	}
	out := make([]enhancer.Message, len(reversed))
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
