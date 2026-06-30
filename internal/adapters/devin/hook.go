// Package devin adapts openPE to the Devin CLI's UserPromptSubmit hook.
//
// Devin CLI uses a Claude-Code-compatible hook format, so the runtime contract
// here mirrors the codex adapter: decode the UserPromptSubmit payload, detect
// the manual `pe` trigger, run the enhancer, and block the original message
// while delivering the enhanced prompt (clipboard + stderr status). The Devin
// CLI is a terminal coding agent like Codex/Claude Code, so the implementation
// approach is intentionally the same.
package devin

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

// UserPromptSubmit is the Devin CLI hook event fired when the user submits a
// message (Claude-Code-compatible name).
const UserPromptSubmit = "UserPromptSubmit"

// clientLabel / cacheNamespace keep this adapter's identity distinct from the
// codex / claude / windsurf adapters (separate cache, separate hook command).
const (
	clientLabel    = "devin"
	cacheNamespace = "devin"
)

type Mode = manual.Mode

const ModePreview = manual.ModePreview

// HookInput is the Devin UserPromptSubmit stdin payload. The lifecycle docs
// only guarantee `prompt`; `cwd` is accepted for forward/Claude compatibility
// but the runtime falls back to DEVIN_PROJECT_DIR / process cwd when absent.
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
	Client   string
	Mode     string
	Auto     bool
	CWD      string
	Prompt   string
	Language string
	Timeout  time.Duration
	History  []enhancer.Message
	// Inject makes a manual `pe` trigger inject the enhanced prompt as
	// additional context (exit 0, JSON hookSpecificOutput) instead of blocking
	// the original message with a clipboard/preview handoff. Devin consumes
	// additionalContext natively. (Codex CLI and Claude Code CLI also consume
	// UserPromptSubmit additionalContext — so the unified inject switch covers
	// them too; only Windsurf cannot, and there the switch is a no-op.) The
	// default (false) preserves the review/clipboard behaviour for all clients.
	Inject bool
	// MaxContextTokens forwards the consumer-layer global token budget
	// (config.Config.MaxContextTokens, sourced from OPENPE_MAX_CONTEXT_TOKENS)
	// into enhancer.Request.Options. Zero means "no budget" so this field is
	// purely additive.
	MaxContextTokens int
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
	if strings.TrimSpace(input.Prompt) == "" {
		return false, false
	}
	_, _, manualTrigger := ParseManualEnhance(input.Prompt)
	return manualTrigger, auto || manualTrigger
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
	manualPrompt, manualMode, manualTrigger := ParseManualEnhance(rawPrompt)
	if !opts.Auto && !manualTrigger {
		return HookOutput{}, nil
	}
	if manualTrigger {
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
		Client:  valueOrDefault(opts.Client, clientLabel),
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
		return HookError(manualTrigger, err.Error(), opts.Language), nil
	}
	if manualTrigger && manualMode == ModePreview && !opts.Inject {
		// Default, controllable path: hold the original (decision=block) and hand
		// the enhanced prompt to the clipboard, exactly like the Codex / Claude /
		// Windsurf clients. The runner overwrites Reason with delivery.HookStatus
		// ("blocked + copied, paste it" / "clipboard failed, see hook last"), so
		// the cross-client experience is consistent. openPE never auto-applies a
		// generated prompt: the user pastes/edits/resubmits it.
		cachePath, _ := SavePreview(resp.EnhancedPrompt, opts.Language)
		return BlockPreview(PreviewReason(cachePath, opts.Language), MarkdownPreview(resp.EnhancedPrompt, opts.Language), resp.EnhancedPrompt), nil
	}
	// Inject mode (auto, or opt-in OPENPE_DEVIN_INJECT): the user has explicitly
	// chosen to trust the enhancement, so inject it as additional context
	// (exit 0). The injection is silent — Devin consumes additionalContext but
	// does not surface our systemMessage — so cache the enhanced prompt too;
	// `openpe devin hook last --prompt` lets the user audit what was injected.
	_, _ = SavePreview(resp.EnhancedPrompt, opts.Language)
	return InjectionOutput(resp.EnhancedPrompt, opts.Language), nil
}

// InjectionOutput builds the UserPromptSubmit output that injects an enhanced
// prompt as additional context (the non-preview success shape). It is exported
// so other adapters can render Devin-native output when the Devin CLI imports
// and runs their hooks: Devin reads additionalContext from stdout JSON, whereas
// the codex/claude/windsurf adapters' native exit-2 + stderr delivery is
// misread by Devin as an empty block.
func InjectionOutput(enhanced string, language string) HookOutput {
	return HookOutput{
		SystemMessage: injectedMessage(language),
		HookSpecificOutput: &HookSpecificOutput{
			HookEventName:     UserPromptSubmit,
			AdditionalContext: AdditionalContext(enhanced),
		},
	}
}

// SkipOutput is the no-op a de-duplication loser emits: it lets the host
// proceed with the original prompt without a second injection or a block, so
// only the single winning hook enhances the prompt.
func SkipOutput() HookOutput {
	return HookOutput{Continue: true}
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

func HookError(manualTrigger bool, message string, language string) HookOutput {
	message = failureMessage(message, language)
	if manualTrigger {
		return Block(message)
	}
	return Skip(message)
}

func PreviewReason(cachePath string, language string) string {
	if isEnglish(language) {
		var b strings.Builder
		b.WriteString("openPE preview generated; original prompt was NOT submitted. ")
		if strings.TrimSpace(cachePath) != "" {
			b.WriteString("Run `openpe devin hook last` to view the full Markdown preview. ")
		}
		return b.String()
	}
	if strings.TrimSpace(cachePath) != "" {
		return "openPE 已生成增强提示词，原始消息未提交。完整预览：openpe devin hook last。"
	}
	return "openPE 已生成增强提示词，原始消息未提交。"
}

func MarkdownPreview(enhanced string, language string) string {
	return preview.Markdown(enhanced, language)
}

func SavePreview(enhanced string, language string) (string, error) {
	cache, err := delivery.Save(cacheNamespace, enhanced, language)
	if err != nil {
		return "", err
	}
	return cache.PreviewPath, nil
}

func ReadLastPreview() (string, error) {
	return delivery.ReadLastPreview(cacheNamespace)
}

func ReadLastPrompt() (string, error) {
	return delivery.ReadLastPrompt(cacheNamespace)
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
	return delivery.LastPreviewPath(cacheNamespace)
}

func LastPromptPath() (string, error) {
	return delivery.LastPromptPath(cacheNamespace)
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
