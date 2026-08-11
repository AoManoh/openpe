package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/AoManoh/openpe/internal/adapters/delivery"
	"github.com/AoManoh/openpe/internal/adapters/manual"
	"github.com/AoManoh/openpe/internal/adapters/preview"
	"github.com/AoManoh/openpe/internal/enhancer"
)

const UserPromptSubmit = "UserPromptSubmit"

type Mode = manual.Mode

const (
	ModePreview = manual.ModePreview
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
	// Warnings carries enhancer.Response.Warnings (out-of-context numbers /
	// undecided actions / language guard) so the runner folds them into the
	// user-facing disclosure before the user acts on the enhancement. Not part
	// of the wire JSON.
	Warnings []string `json:"-"`
}

type HookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext,omitempty"`
}

type HookOptions struct {
	Client   string
	Mode     string
	Auto     bool
	CWD      string
	Prompt   string
	Language string
	Timeout  time.Duration
	History  []enhancer.Message
	// Inject makes a manual `pe` trigger inject the enhanced prompt as
	// additional context (exit 0, hookSpecificOutput.additionalContext) instead
	// of holding the original with a clipboard/preview review. Codex CLI
	// consumes UserPromptSubmit additionalContext into the model context, so
	// this is a real injection there. Default false preserves the review model.
	Inject bool
	// MaxContextTokens forwards the consumer-layer global token budget
	// (config.Config.MaxContextTokens, sourced from
	// OPENPE_MAX_CONTEXT_TOKENS) into enhancer.Request.Options. Zero
	// means "no budget" so this field is purely additive — callers that
	// do not set it preserve the historical unbounded behaviour.
	MaxContextTokens int
	CacheDir         string
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
		return Block(emptyPromptMessage(opts.Language)), nil
	}
	cwd := strings.TrimSpace(input.CWD)
	if opts.CWD != "" {
		cwd = strings.TrimSpace(opts.CWD)
	}
	req := enhancer.Request{
		Prompt:  rawPrompt,
		Client:  valueOrDefault(opts.Client, "codex"),
		CWD:     cwd,
		Mode:    valueOrDefault(opts.Mode, "agent"),
		History: opts.History,
		Options: enhancer.Options{MaxContextTokens: opts.MaxContextTokens},
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	resp, err := service.Enhance(ctx, req)
	if err != nil {
		return HookError(manual, err.Error(), opts.Language), nil
	}
	if manual && manualMode == ModePreview && !opts.Inject {
		cachePath, _ := savePreview(resp.EnhancedPrompt, opts.Language, opts.CacheDir)
		out := BlockPreview(PreviewReason(cachePath, opts.Language), MarkdownPreview(resp.EnhancedPrompt, opts.Language), resp.EnhancedPrompt)
		out.Warnings = resp.Warnings
		return out, nil
	}
	// Inject mode (--auto, or OPENPE_HOOK_INJECT/OPENPE_CODEX_INJECT): cache the
	// enhanced prompt for audit (`openpe codex hook last --prompt`), then inject
	// it as additional context.
	_, _ = savePreview(resp.EnhancedPrompt, opts.Language, opts.CacheDir)
	return HookOutput{
		SystemMessage: injectedMessage(opts.Language),
		HookSpecificOutput: &HookSpecificOutput{
			HookEventName:     UserPromptSubmit,
			AdditionalContext: AdditionalContext(resp.EnhancedPrompt),
		},
		Warnings: resp.Warnings,
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

func HookError(manual bool, message string, language string) HookOutput {
	message = failureMessage(message, language)
	if manual {
		return Block(message)
	}
	return Skip(message)
}

func PreviewReason(cachePath string, language string) string {
	if isEnglish(language) {
		var b strings.Builder
		b.WriteString("openPE preview generated; original prompt was NOT submitted. ")
		if strings.TrimSpace(cachePath) != "" {
			b.WriteString("Run `openpe codex hook last` to view the full Markdown preview. ")
		}
		return b.String()
	}
	if strings.TrimSpace(cachePath) != "" {
		return "openPE 已生成增强提示词，原始消息未提交。完整预览：openpe codex hook last。"
	}
	return "openPE 已生成增强提示词，原始消息未提交。"
}

func MarkdownPreview(enhanced string, language string) string {
	return preview.Markdown(enhanced, language)
}

func SavePreview(enhanced string, language string) (string, error) {
	return savePreview(enhanced, language, "")
}

func savePreview(enhanced string, language string, cacheDir string) (string, error) {
	cache, err := delivery.SaveWithOptions("codex", enhanced, language, delivery.Options{CacheDir: cacheDir})
	if err != nil {
		return "", err
	}
	return cache.PreviewPath, nil
}

func ReadLastPreview() (string, error) {
	return delivery.ReadLastPreview("codex")
}

func ReadLastPrompt() (string, error) {
	return delivery.ReadLastPrompt("codex")
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
	return delivery.LastPreviewPath("codex")
}

func LastPromptPath() (string, error) {
	return delivery.LastPromptPath("codex")
}

func AdditionalContext(enhanced string) string {
	return strings.TrimSpace(`openPE generated an enhanced version of the user's prompt.

Use this enhanced prompt as the preferred interpretation of the user's request while preserving any explicit user constraints and safety boundaries.

<openpe_enhanced_prompt>
` + strings.TrimSpace(enhanced) + `
</openpe_enhanced_prompt>`)
}

func isEnglish(language string) bool {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "en", "en-us", "english":
		return true
	default:
		return false
	}
}

func emptyPromptMessage(language string) string {
	if isEnglish(language) {
		return "openPE trigger found, but prompt is empty."
	}
	return "openPE 触发词后缺少要增强的内容。"
}

func failureMessage(message string, language string) string {
	if isEnglish(language) {
		return "openPE prompt enhancement failed: " + message
	}
	return "openPE 增强失败：" + message
}

func injectedMessage(language string) string {
	if isEnglish(language) {
		return "openPE enhanced prompt injected as additional context."
	}
	return "openPE 已将增强提示词注入为附加上下文。"
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
