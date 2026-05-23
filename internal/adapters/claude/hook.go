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
	// MaxContextTokens forwards the consumer-layer global token budget
	// (config.Config.MaxContextTokens, sourced from
	// OPENPE_MAX_CONTEXT_TOKENS) into enhancer.Request.Options. Zero
	// means "no budget" so this field is purely additive — callers that
	// do not set it preserve the historical unbounded behaviour.
	MaxContextTokens int
}

type HookOutput struct {
	TerminalPreview string
	PreviewPrompt   string
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
	_, _, ok := manual.Parse(input.Prompt)
	return ok
}

func HandleHook(ctx context.Context, service *enhancer.Service, input HookInput, opts HookOptions) (HookOutput, error) {
	if input.HookEventName != "" && input.HookEventName != UserPromptSubmit {
		return HookOutput{}, nil
	}
	rawPrompt, _, manualTrigger := manual.Parse(input.Prompt)
	if !manualTrigger {
		return HookOutput{}, nil
	}
	if strings.TrimSpace(rawPrompt) == "" {
		return HookOutput{}, errors.New(emptyPromptMessage(opts.Language))
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
	return HookOutput{
		TerminalPreview: preview.Markdown(resp.EnhancedPrompt, opts.Language),
		PreviewPrompt:   strings.TrimSpace(resp.EnhancedPrompt),
	}, nil
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
