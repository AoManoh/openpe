package claude

import (
	"context"
	"encoding/json"
	"fmt"
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
	Client  string
	Mode    string
	CWD     string
	Timeout time.Duration
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

func HandleHook(ctx context.Context, service *enhancer.Service, input HookInput, opts HookOptions) (string, error) {
	if input.HookEventName != "" && input.HookEventName != UserPromptSubmit {
		return "", nil
	}
	rawPrompt, _, manualTrigger := manual.Parse(input.Prompt)
	if !manualTrigger {
		return "", nil
	}
	if strings.TrimSpace(rawPrompt) == "" {
		return "", fmt.Errorf("openPE trigger found, but prompt is empty")
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
		Prompt: rawPrompt,
		Client: valueOrDefault(opts.Client, "claude-code"),
		CWD:    cwd,
		Mode:   valueOrDefault(opts.Mode, "agent"),
	})
	if err != nil {
		return "", err
	}
	return preview.Markdown(resp.EnhancedPrompt), nil
}

func valueOrDefault(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
