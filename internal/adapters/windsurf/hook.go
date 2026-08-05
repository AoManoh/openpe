package windsurf

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/AoManoh/openpe/internal/adapters/delivery"
	"github.com/AoManoh/openpe/internal/adapters/manual"
	"github.com/AoManoh/openpe/internal/adapters/preview"
	"github.com/AoManoh/openpe/internal/enhancer"
)

const PreUserPrompt = "pre_user_prompt"

type ToolInfo struct {
	UserPrompt string `json:"user_prompt"`
}

type HookInput struct {
	AgentActionName string   `json:"agent_action_name"`
	CWD             string   `json:"cwd"`
	ToolInfo        ToolInfo `json:"tool_info"`
}

type HookOptions struct {
	Client   string
	Mode     string
	CWD      string
	Language string
	Timeout  time.Duration
	// MaxContextTokens forwards the consumer-layer global token budget
	// (config.Config.MaxContextTokens, sourced from
	// OPENPE_MAX_CONTEXT_TOKENS) into enhancer.Request.Options. Zero
	// means "no budget" so this field is purely additive — callers that
	// do not set it preserve the historical unbounded behaviour.
	// Windsurf hook has no History field because Cascade exposes no
	// public file-based session log to the hook subprocess (see
	// cascade_context.ts in the patch sub-project for the renderer-side
	// best-effort path); MaxContextTokens still applies to any rules /
	// guidelines / context.files / context.retrieval that future caller
	// paths attach to the request.
	MaxContextTokens int
	CacheDir         string
}

type HookOutput struct {
	TerminalPreview string
	PreviewPrompt   string
	CachePath       string
}

func DecodeHookInput(r io.Reader) (HookInput, error) {
	var input HookInput
	if err := json.NewDecoder(r).Decode(&input); err != nil {
		return HookInput{}, err
	}
	return input, nil
}

func ShouldHandleHook(input HookInput) bool {
	if input.AgentActionName != "" && input.AgentActionName != PreUserPrompt {
		return false
	}
	_, _, ok := manual.Parse(input.ToolInfo.UserPrompt)
	return ok
}

func HandleHook(ctx context.Context, service *enhancer.Service, input HookInput, opts HookOptions) (HookOutput, error) {
	if input.AgentActionName != "" && input.AgentActionName != PreUserPrompt {
		return HookOutput{}, nil
	}
	rawPrompt, _, manualTrigger := manual.Parse(input.ToolInfo.UserPrompt)
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
		Client:  valueOrDefault(opts.Client, "windsurf"),
		CWD:     cwd,
		Mode:    valueOrDefault(opts.Mode, "cascade"),
		Options: enhancer.Options{MaxContextTokens: opts.MaxContextTokens},
	})
	if err != nil {
		return HookOutput{}, err
	}
	cachePath, _ := savePreview(resp.EnhancedPrompt, opts.Language, opts.CacheDir)
	return HookOutput{
		TerminalPreview: preview.Markdown(resp.EnhancedPrompt, opts.Language),
		PreviewPrompt:   strings.TrimSpace(resp.EnhancedPrompt),
		CachePath:       cachePath,
	}, nil
}

func SavePreview(enhanced string, language string) (string, error) {
	return savePreview(enhanced, language, "")
}

func savePreview(enhanced string, language string, cacheDir string) (string, error) {
	cache, err := delivery.SaveWithOptions("windsurf", enhanced, language, delivery.Options{CacheDir: cacheDir})
	if err != nil {
		return "", err
	}
	return cache.PreviewPath, nil
}

func ReadLastPreview() (string, error) {
	return delivery.ReadLastPreview("windsurf")
}

func ReadLastPrompt() (string, error) {
	return delivery.ReadLastPrompt("windsurf")
}

func LastPreviewPath() (string, error) {
	return delivery.LastPreviewPath("windsurf")
}

func LastPromptPath() (string, error) {
	return delivery.LastPromptPath("windsurf")
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
