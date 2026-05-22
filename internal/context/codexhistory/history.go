package codexhistory

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/AoManoh/openpe/internal/enhancer"
)

const (
	defaultMaxMessages = 12
	defaultMaxChars    = 12000
	maxScannerBytes    = 4 * 1024 * 1024
)

type Options struct {
	Home        string
	MaxMessages int
	MaxChars    int
}

type Result struct {
	SessionID   string
	RolloutPath string
	Messages    []enhancer.Message
}

type Collector struct {
	home        string
	maxMessages int
	maxChars    int
}

func New(opts Options) *Collector {
	home := strings.TrimSpace(opts.Home)
	if home == "" {
		home = defaultHome()
	}
	maxMessages := opts.MaxMessages
	if maxMessages <= 0 {
		maxMessages = defaultMaxMessages
	}
	maxChars := opts.MaxChars
	if maxChars <= 0 {
		maxChars = defaultMaxChars
	}
	return &Collector{home: home, maxMessages: maxMessages, maxChars: maxChars}
}

func (c *Collector) Retrieve(prompt string, cwd string) (Result, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return Result{}, nil
	}
	entries, currentIndex, err := c.historyEntries(prompt)
	if err != nil || currentIndex < 0 {
		return Result{}, err
	}
	current := entries[currentIndex]
	if strings.TrimSpace(current.SessionID) == "" {
		return Result{}, nil
	}

	rolloutPath, err := findRolloutPath(c.home, current.SessionID)
	if err != nil || rolloutPath == "" {
		return Result{SessionID: current.SessionID}, nil
	}
	messages, ok, err := readRolloutMessages(rolloutPath, cwd)
	if err != nil || !ok {
		return Result{SessionID: current.SessionID, RolloutPath: rolloutPath}, nil
	}
	return Result{
		SessionID:   current.SessionID,
		RolloutPath: rolloutPath,
		Messages:    limitMessages(messages, c.maxMessages, c.maxChars),
	}, nil
}

type historyEntry struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
	TS        int64  `json:"ts"`
}

func (c *Collector) historyEntries(prompt string) ([]historyEntry, int, error) {
	path := filepath.Join(c.home, "history.jsonl")
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, -1, nil
		}
		return nil, -1, err
	}
	defer file.Close()

	var entries []historyEntry
	currentIndex := -1
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxScannerBytes)
	for scanner.Scan() {
		var entry historyEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
		if strings.TrimSpace(entry.Text) == prompt {
			currentIndex = len(entries) - 1
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, -1, err
	}
	return entries, currentIndex, nil
}

func findRolloutPath(home string, sessionID string) (string, error) {
	root := filepath.Join(home, "sessions")
	var found string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || found != "" {
			return nil
		}
		name := d.Name()
		if strings.Contains(name, sessionID) && strings.HasSuffix(name, ".jsonl") {
			found = path
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return found, nil
}

type rolloutEnvelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type rolloutMeta struct {
	CWD string `json:"cwd"`
}

type rolloutMessage struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func readRolloutMessages(path string, cwd string) ([]enhancer.Message, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()

	var messages []enhancer.Message
	cwdMatched := strings.TrimSpace(cwd) == ""
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxScannerBytes)
	for scanner.Scan() {
		var env rolloutEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &env); err != nil {
			continue
		}
		if env.Type == "session_meta" {
			var meta rolloutMeta
			if json.Unmarshal(env.Payload, &meta) == nil && samePath(meta.CWD, cwd) {
				cwdMatched = true
			}
			continue
		}
		var msg rolloutMessage
		if err := json.Unmarshal(env.Payload, &msg); err != nil || msg.Type != "message" {
			continue
		}
		role := strings.TrimSpace(msg.Role)
		if role != "user" && role != "assistant" {
			continue
		}
		content := extractContent(msg.Content)
		if content == "" {
			continue
		}
		messages = append(messages, enhancer.Message{Role: role, Content: content})
	}
	if err := scanner.Err(); err != nil {
		return nil, false, err
	}
	return messages, cwdMatched, nil
}

func extractContent(raw json.RawMessage) string {
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
		msg := messages[i]
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		if runeLen(content) > remaining {
			content = truncateRunes(content, remaining)
		}
		reversed = append(reversed, enhancer.Message{Role: msg.Role, Content: content})
		remaining -= runeLen(content)
	}
	out := make([]enhancer.Message, len(reversed))
	for i := range reversed {
		out[len(reversed)-1-i] = reversed[i]
	}
	return out
}

func samePath(a string, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA == nil {
		a = filepath.Clean(aa)
	}
	if errB == nil {
		b = filepath.Clean(bb)
	}
	return a == b
}

func defaultHome() string {
	if value := strings.TrimSpace(os.Getenv("CODEX_HOME")); value != "" {
		return value
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".codex")
	}
	return ".codex"
}

func runeLen(value string) int {
	return utf8.RuneCountInString(value)
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	var b strings.Builder
	count := 0
	for _, r := range value {
		if count >= limit {
			break
		}
		b.WriteRune(r)
		count++
	}
	return b.String()
}
