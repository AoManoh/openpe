package claudetranscript

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/AoManoh/openpe/internal/context/histstatus"
	"github.com/AoManoh/openpe/internal/enhancer"
)

const (
	defaultMaxMessages = 12
	defaultMaxChars    = 12000
	maxScannerBytes    = 4 * 1024 * 1024
)

type Options struct {
	MaxMessages int
	MaxChars    int
}

type Result struct {
	TranscriptPath string
	Status         histstatus.Status
	Messages       []enhancer.Message
}

type Collector struct {
	maxMessages int
	maxChars    int
}

func New(opts Options) *Collector {
	maxMessages := opts.MaxMessages
	if maxMessages <= 0 {
		maxMessages = defaultMaxMessages
	}
	maxChars := opts.MaxChars
	if maxChars <= 0 {
		maxChars = defaultMaxChars
	}
	return &Collector{maxMessages: maxMessages, maxChars: maxChars}
}

func (c *Collector) Retrieve(transcriptPath string, cwd string) (Result, error) {
	transcriptPath = strings.TrimSpace(transcriptPath)
	if transcriptPath == "" {
		return Result{Status: histstatus.NoSession}, nil
	}
	if _, err := os.Stat(transcriptPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No transcript at the hook-provided path: explicit "no history".
			return Result{TranscriptPath: transcriptPath, Status: histstatus.NoSession}, nil
		}
		return Result{TranscriptPath: transcriptPath}, err
	}
	messages, cwdMatched, err := readTranscriptMessages(transcriptPath, cwd)
	if err != nil {
		return Result{TranscriptPath: transcriptPath}, err
	}
	if !cwdMatched {
		// Transcript belongs to another workspace.
		return Result{TranscriptPath: transcriptPath, Status: histstatus.CWDMismatch}, nil
	}
	limited := limitMessages(messages, c.maxMessages, c.maxChars)
	status := histstatus.Found
	if len(limited) == 0 {
		status = histstatus.Empty
	}
	return Result{TranscriptPath: transcriptPath, Status: status, Messages: limited}, nil
}

type transcriptEntry struct {
	Type    string            `json:"type"`
	CWD     string            `json:"cwd"`
	Message transcriptMessage `json:"message"`
}

type transcriptMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func readTranscriptMessages(path string, cwd string) ([]enhancer.Message, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer file.Close()

	cwdMatched := strings.TrimSpace(cwd) == ""
	var messages []enhancer.Message
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxScannerBytes)
	for scanner.Scan() {
		var entry transcriptEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if samePath(entry.CWD, cwd) {
			cwdMatched = true
		}
		role := strings.TrimSpace(entry.Message.Role)
		if role == "" {
			role = strings.TrimSpace(entry.Type)
		}
		if role != "user" && role != "assistant" {
			continue
		}
		content := extractTextContent(entry.Message.Content)
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

func extractTextContent(raw json.RawMessage) string {
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
		if strings.TrimSpace(stringValue(part["type"])) != "text" {
			continue
		}
		text := strings.TrimSpace(stringValue(part["text"]))
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(text)
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

func stringValue(value any) string {
	text, _ := value.(string)
	return text
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
