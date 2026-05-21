package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AoManoh/openpe/internal/adapters/manual"
	"github.com/AoManoh/openpe/internal/adapters/preview"
	"github.com/AoManoh/openpe/internal/enhancer"
)

const UserPromptSubmit = "UserPromptSubmit"

type Mode = manual.Mode

const (
	ModePreview = manual.ModePreview
	ModeInject  = manual.ModeInject
)

type HookInput struct {
	HookEventName string `json:"hook_event_name"`
	Prompt        string `json:"prompt"`
	CWD           string `json:"cwd"`
}

type HookOutput struct {
	Continue           bool                `json:"continue,omitempty"`
	Decision           string              `json:"decision,omitempty"`
	Reason             string              `json:"reason,omitempty"`
	SystemMessage      string              `json:"systemMessage,omitempty"`
	HookSpecificOutput *HookSpecificOutput `json:"hookSpecificOutput,omitempty"`
	TerminalPreview    string              `json:"-"`
	PreviewPrompt      string              `json:"-"`
}

type HookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext,omitempty"`
}

type HookOptions struct {
	Client  string
	Mode    string
	Auto    bool
	CWD     string
	Prompt  string
	Timeout time.Duration
}

func DecodeHookInput(r io.Reader) (HookInput, error) {
	var input HookInput
	if err := json.NewDecoder(r).Decode(&input); err != nil {
		return HookInput{}, err
	}
	return input, nil
}

func ShouldHandleHook(input HookInput, auto bool) (bool, bool) {
	if input.HookEventName != "" && input.HookEventName != UserPromptSubmit {
		return false, false
	}
	rawPrompt := strings.TrimSpace(input.Prompt)
	if rawPrompt == "" {
		return false, false
	}
	_, _, manual := ParseManualEnhance(rawPrompt)
	return manual, auto || manual
}

func EncodeHookOutput(w io.Writer, output HookOutput) error {
	return json.NewEncoder(w).Encode(output)
}

func HandleHook(ctx context.Context, service *enhancer.Service, input HookInput, opts HookOptions) (HookOutput, error) {
	if input.HookEventName != "" && input.HookEventName != UserPromptSubmit {
		return HookOutput{}, nil
	}
	rawPrompt := strings.TrimSpace(input.Prompt)
	if opts.Prompt != "" {
		rawPrompt = strings.TrimSpace(opts.Prompt)
	}
	if rawPrompt == "" {
		return HookOutput{}, nil
	}
	manualPrompt, manualMode, manual := ParseManualEnhance(rawPrompt)
	if !opts.Auto && !manual {
		return HookOutput{}, nil
	}
	if manual {
		rawPrompt = manualPrompt
	}
	if rawPrompt == "" {
		return Block("openPE trigger found, but prompt is empty."), nil
	}
	cwd := strings.TrimSpace(input.CWD)
	if opts.CWD != "" {
		cwd = strings.TrimSpace(opts.CWD)
	}
	req := enhancer.Request{
		Prompt: rawPrompt,
		Client: valueOrDefault(opts.Client, "codex"),
		CWD:    cwd,
		Mode:   valueOrDefault(opts.Mode, "agent"),
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	resp, err := service.Enhance(ctx, req)
	if err != nil {
		return HookError(manual, err.Error()), nil
	}
	if manual && manualMode == ModePreview {
		cachePath, _ := SavePreview(resp.EnhancedPrompt)
		return BlockPreview(PreviewReason(cachePath), MarkdownPreview(resp.EnhancedPrompt), resp.EnhancedPrompt), nil
	}
	return HookOutput{
		SystemMessage: "openPE enhanced prompt injected as additional context.",
		HookSpecificOutput: &HookSpecificOutput{
			HookEventName:     UserPromptSubmit,
			AdditionalContext: AdditionalContext(resp.EnhancedPrompt),
		},
	}, nil
}

func ParseManualEnhance(prompt string) (string, Mode, bool) {
	return manual.Parse(prompt)
}

func Block(reason string) HookOutput {
	return HookOutput{Decision: "block", Reason: reason}
}

func BlockPreview(reason string, terminalPreview string, previewPrompt string) HookOutput {
	return HookOutput{
		Decision:        "block",
		Reason:          strings.TrimSpace(reason),
		TerminalPreview: strings.TrimSpace(terminalPreview),
		PreviewPrompt:   strings.TrimSpace(previewPrompt),
	}
}

func Skip(message string) HookOutput {
	return HookOutput{Continue: true, SystemMessage: message}
}

func HookError(manual bool, message string) HookOutput {
	message = "openPE prompt enhancement failed: " + message
	if manual {
		return Block(message)
	}
	return Skip(message)
}

func PreviewReason(cachePath string) string {
	var b strings.Builder
	b.WriteString("openPE preview generated; original prompt was NOT submitted. ")
	if strings.TrimSpace(cachePath) != "" {
		b.WriteString("Run `openpe codex hook last` to view the full Markdown preview. ")
	}
	return b.String()
}

func AppendClipboardStatus(reason string, method string, err error) string {
	reason = strings.TrimSpace(reason)
	if err == nil && strings.TrimSpace(method) != "" {
		if strings.EqualFold(strings.TrimSpace(method), "OSC52") {
			return reason + " OSC52 clipboard sequence sent; if your terminal supports it, paste the enhanced prompt into the input box to edit and send."
		}
		return reason + " Enhanced prompt copied to clipboard; paste it into the input box to edit and send."
	}
	if err != nil {
		return reason + " Clipboard copy unavailable; use `openpe codex hook last` as fallback."
	}
	return reason
}

func MarkdownPreview(enhanced string) string {
	return preview.Markdown(enhanced)
}

func SavePreview(enhanced string) (string, error) {
	dir, err := previewDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "last.md")
	if err := os.WriteFile(path, []byte(MarkdownPreview(enhanced)+"\n"), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func ReadLastPreview() (string, error) {
	path, err := LastPreviewPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func WriteTerminalPreview(content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer tty.Close()
	_, err = fmt.Fprint(tty, "\r\n"+toCRLF(content)+"\r\n\r\n")
	return err
}

func toCRLF(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\n", "\r\n")
}

func LastPreviewPath() (string, error) {
	dir, err := previewDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "last.md"), nil
}

func previewDir() (string, error) {
	if value := strings.TrimSpace(os.Getenv("OPENPE_CACHE_DIR")); value != "" {
		return value, nil
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "openpe", "codex"), nil
}

func compactPreview(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if limit > 0 && len([]rune(value)) > limit {
		return string([]rune(value)[:limit]) + "..."
	}
	return value
}

func AdditionalContext(enhanced string) string {
	return strings.TrimSpace(`openPE generated an enhanced version of the user's prompt.

Use this enhanced prompt as the preferred interpretation of the user's request while preserving any explicit user constraints and safety boundaries.

<openpe_enhanced_prompt>
` + strings.TrimSpace(enhanced) + `
</openpe_enhanced_prompt>`)
}

func valueOrDefault(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func EncodeHookOutputOrFallback(w io.Writer, output HookOutput) int {
	if err := EncodeHookOutput(w, output); err != nil {
		_, _ = fmt.Fprintf(w, `{"continue":true,"systemMessage":"openPE failed to encode hook output: %v"}`+"\n", err)
		return 1
	}
	return 0
}
