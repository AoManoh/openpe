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

	"github.com/AoManoh/openpe/internal/enhancer"
)

const UserPromptSubmit = "UserPromptSubmit"

type Mode string

const (
	ModePreview Mode = "preview"
	ModeInject  Mode = "inject"
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
		return Block(PreviewReason(resp.EnhancedPrompt, cachePath)), nil
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
	prompt = strings.TrimSpace(prompt)
	for _, trigger := range []struct {
		prefix string
		mode   Mode
	}{
		{prefix: "pe!:", mode: ModeInject},
		{prefix: "pe！：", mode: ModeInject},
		{prefix: "pe!：", mode: ModeInject},
		{prefix: "pe！:", mode: ModeInject},
		{prefix: "openpe!:", mode: ModeInject},
		{prefix: "openpe！：", mode: ModeInject},
		{prefix: "openpe!：", mode: ModeInject},
		{prefix: "openpe！:", mode: ModeInject},
		{prefix: "增强!:", mode: ModeInject},
		{prefix: "增强！：", mode: ModeInject},
		{prefix: "增强!：", mode: ModeInject},
		{prefix: "增强！:", mode: ModeInject},
		{prefix: "pe:", mode: ModePreview},
		{prefix: "pe：", mode: ModePreview},
		{prefix: "openpe:", mode: ModePreview},
		{prefix: "openpe：", mode: ModePreview},
		{prefix: "增强:", mode: ModePreview},
		{prefix: "增强：", mode: ModePreview},
	} {
		if strings.HasPrefix(prompt, trigger.prefix) {
			return strings.TrimSpace(strings.TrimPrefix(prompt, trigger.prefix)), trigger.mode, true
		}
	}
	return prompt, "", false
}

func Block(reason string) HookOutput {
	return HookOutput{Decision: "block", Reason: reason}
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

func PreviewReason(enhanced string, cachePath string) string {
	enhanced = strings.TrimSpace(enhanced)
	summary := compactPreview(enhanced, 900)
	var b strings.Builder
	b.WriteString("openPE preview generated; original prompt was NOT submitted. ")
	if strings.TrimSpace(cachePath) != "" {
		b.WriteString("Run `openpe codex last` to view the full Markdown preview. ")
	}
	b.WriteString("COPY BELOW >>> ")
	b.WriteString(summary)
	b.WriteString(" <<< COPY ABOVE")
	return b.String()
}

func MarkdownPreview(enhanced string) string {
	enhanced = strings.TrimSpace(enhanced)
	return strings.TrimSpace(`# openPE Enhanced Prompt

> This preview was not submitted to the model. Copy, edit, and send it manually when ready.

` + "```markdown\n" + enhanced + "\n```")
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
	if limit > 0 && len(value) > limit {
		return value[:limit] + "..."
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
