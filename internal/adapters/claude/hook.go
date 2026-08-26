package claude

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/AoManoh/openpe/internal/adapters/manual"
	"github.com/AoManoh/openpe/internal/adapters/preview"
	"github.com/AoManoh/openpe/internal/enhancer"
	"github.com/AoManoh/openpe/internal/specs"
)

const UserPromptSubmit = "UserPromptSubmit"

type HookInput struct {
	HookEventName  string `json:"hook_event_name"`
	Prompt         string `json:"prompt"`
	CWD            string `json:"cwd"`
	TranscriptPath string `json:"transcript_path"`
}

type HookOptions struct {
	Client   string
	Mode     string
	CWD      string
	Language string
	Timeout  time.Duration
	History  []enhancer.Message
	// Inject makes a manual `pe` trigger inject the enhanced prompt as additional
	// context (exit 0, hookSpecificOutput.additionalContext) instead of the
	// exit-2 + clipboard review delivery. Claude Code (CLI) wraps additionalContext
	// in a system-reminder and feeds it to the model. Default false preserves the
	// review model. (Note: the Claude VSCode extension does not consume it.)
	Inject bool
	// MaxContextTokens forwards the consumer-layer global token budget
	// (config.Config.MaxContextTokens, sourced from
	// OPENPE_MAX_CONTEXT_TOKENS) into enhancer.Request.Options. Zero
	// means "no budget" so this field is purely additive — callers that
	// do not set it preserve the historical unbounded behaviour.
	MaxContextTokens int
	CacheDir         string
	// SpecsDir / SpecMaxChars configure explicit user prompt-spec loading
	// (`pe+<name> <task>`, config.Config.Specs). Empty dir means the per-user
	// default ~/.config/openpe/specs; MaxChars <= 0 means the specs package
	// default. Spec resolution failures BLOCK the enhancement (business
	// contract D7: never silently drop a user-named spec).
	SpecsDir     string
	SpecMaxChars int
}

type HookOutput struct {
	TerminalPreview string
	PreviewPrompt   string
	// Injected is set when opts.Inject: the caller emits exit-0 JSON carrying
	// HookSpecificOutput.additionalContext (and SystemMessage) instead of the
	// exit-2 + clipboard review path.
	Injected           bool
	SystemMessage      string
	HookSpecificOutput *HookSpecificOutput
	// Warnings carries enhancer.Response.Warnings (out-of-context numbers /
	// undecided actions / language guard) so the runner folds them into the
	// user-facing disclosure before the user acts on the enhancement.
	Warnings []string
	// AppliedSpecs lists the user spec names appended to the enhanced prompt
	// (`pe+<name>`), so the runner can disclose "applied specs: …" alongside
	// the delivery status.
	AppliedSpecs []string
}

type HookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext,omitempty"`
}

func DecodeHookInput(r io.Reader) (HookInput, error) {
	var input HookInput
	if err := json.NewDecoder(r).Decode(&input); err != nil {
		return HookInput{}, err
	}
	return input, nil
}

func ShouldHandleHook(input HookInput) bool {
	if input.HookEventName != "" && input.HookEventName != UserPromptSubmit {
		return false
	}
	_, _, _, ok := manual.Parse(input.Prompt)
	return ok
}

func HandleHook(ctx context.Context, service *enhancer.Service, input HookInput, opts HookOptions) (HookOutput, error) {
	if input.HookEventName != "" && input.HookEventName != UserPromptSubmit {
		return HookOutput{}, nil
	}
	rawPrompt, specNames, _, manualTrigger := manual.Parse(input.Prompt)
	if !manualTrigger {
		return HookOutput{}, nil
	}
	if strings.TrimSpace(rawPrompt) == "" {
		return HookOutput{}, errors.New(emptyPromptMessage(opts.Language))
	}
	// 用户点名的规范在调用模型之前解析：失败立即阻断（零 token 消耗），
	// 结构上不存在"增强了但没带规范"的静默输出。
	loadedSpecs, specErr := specs.LoadWithDefaults(opts.SpecsDir, specNames, opts.SpecMaxChars)
	if specErr != nil {
		return HookOutput{}, errors.New(specs.ErrorMessage(specErr, opts.Language))
	}
	cwd := strings.TrimSpace(input.CWD)
	if strings.TrimSpace(opts.CWD) != "" {
		cwd = strings.TrimSpace(opts.CWD)
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	resp, err := service.Enhance(ctx, enhancer.Request{
		Prompt:  rawPrompt,
		Client:  valueOrDefault(opts.Client, "claude-code"),
		CWD:     cwd,
		Mode:    valueOrDefault(opts.Mode, "agent"),
		History: opts.History,
		Options: enhancer.Options{MaxContextTokens: opts.MaxContextTokens},
	})
	if err != nil {
		return HookOutput{}, err
	}
	// a1 机械追加：规范原文块拼接在模型输出之后，预览/注入统一使用追加后的文本。
	enhanced := specs.Append(resp.EnhancedPrompt, loadedSpecs, opts.Language)
	if opts.Inject {
		return HookOutput{
			Injected:      true,
			SystemMessage: injectedMessage(opts.Language),
			HookSpecificOutput: &HookSpecificOutput{
				HookEventName:     UserPromptSubmit,
				AdditionalContext: AdditionalContext(enhanced),
			},
			PreviewPrompt: strings.TrimSpace(enhanced),
			Warnings:      resp.Warnings,
			AppliedSpecs:  specs.Names(loadedSpecs),
		}, nil
	}
	return HookOutput{
		TerminalPreview: preview.Markdown(enhanced, opts.Language),
		PreviewPrompt:   strings.TrimSpace(enhanced),
		Warnings:        resp.Warnings,
		AppliedSpecs:    specs.Names(loadedSpecs),
	}, nil
}

// AdditionalContext wraps the enhanced prompt for hook-context injection,
// matching the codex/devin adapters' wrapper so the cross-client behaviour is
// consistent.
func AdditionalContext(enhanced string) string {
	return strings.TrimSpace(`openPE generated an enhanced version of the user's prompt.

Use this enhanced prompt as the preferred interpretation of the user's request while preserving any explicit user constraints and safety boundaries.

<openpe_enhanced_prompt>
` + strings.TrimSpace(enhanced) + `
</openpe_enhanced_prompt>`)
}

// EncodeInjection writes the Claude UserPromptSubmit exit-0 JSON that injects
// additionalContext (and an optional systemMessage) into Claude's context.
func EncodeInjection(w io.Writer, output HookOutput) error {
	payload := struct {
		SystemMessage      string              `json:"systemMessage,omitempty"`
		HookSpecificOutput *HookSpecificOutput `json:"hookSpecificOutput,omitempty"`
	}{
		SystemMessage:      strings.TrimSpace(output.SystemMessage),
		HookSpecificOutput: output.HookSpecificOutput,
	}
	return json.NewEncoder(w).Encode(payload)
}

func injectedMessage(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "en", "en-us", "english":
		return "openPE enhanced prompt injected as additional context."
	default:
		return "openPE 已将增强提示词注入为附加上下文。"
	}
}

func valueOrDefault(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func emptyPromptMessage(language string) string {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "en", "en-us", "english":
		return "openPE trigger found, but prompt is empty"
	default:
		return "openPE 触发词后缺少要增强的内容"
	}
}
