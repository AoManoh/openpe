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
	"github.com/AoManoh/openpe/internal/specs"
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
	CachePath       string
	// Warnings carries enhancer.Response.Warnings (out-of-context numbers /
	// undecided actions / language guard) so the runner folds them into the
	// user-facing disclosure before the user acts on the enhancement.
	Warnings []string
	// AppliedSpecs lists the user spec names appended to the enhanced prompt
	// (`pe+<name>`), so the runner can disclose "applied specs: …" alongside
	// the delivery status.
	AppliedSpecs []string
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
	_, _, _, ok := manual.Parse(input.ToolInfo.UserPrompt)
	return ok
}

func HandleHook(ctx context.Context, service *enhancer.Service, input HookInput, opts HookOptions) (HookOutput, error) {
	if input.AgentActionName != "" && input.AgentActionName != PreUserPrompt {
		return HookOutput{}, nil
	}
	rawPrompt, specNames, _, manualTrigger := manual.Parse(input.ToolInfo.UserPrompt)
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
		Client:  valueOrDefault(opts.Client, "windsurf"),
		CWD:     cwd,
		Mode:    valueOrDefault(opts.Mode, "cascade"),
		Options: enhancer.Options{MaxContextTokens: opts.MaxContextTokens},
	})
	if err != nil {
		return HookOutput{}, err
	}
	// a1 机械追加：规范原文块拼接在模型输出之后，缓存/预览统一使用追加后的文本。
	enhanced := specs.Append(resp.EnhancedPrompt, loadedSpecs, opts.Language)
	cachePath, _ := savePreview(enhanced, opts.Language, opts.CacheDir)
	return HookOutput{
		TerminalPreview: preview.Markdown(enhanced, opts.Language),
		PreviewPrompt:   strings.TrimSpace(enhanced),
		CachePath:       cachePath,
		Warnings:        resp.Warnings,
		AppliedSpecs:    specs.Names(loadedSpecs),
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
