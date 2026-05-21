package codex

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

func HookCommand(bin string, envFile string) string {
	command := shellQuote(bin) + " codex hook run"
	if strings.TrimSpace(envFile) == "" {
		return command
	}
	return "OPENPE_ENV_FILE=" + shellQuote(envFile) + " " + command
}

func MergeHooksConfig(existing []byte, command string, timeout int) ([]byte, error) {
	root := map[string]any{}
	if len(strings.TrimSpace(string(existing))) > 0 {
		if err := json.Unmarshal(existing, &root); err != nil {
			return nil, err
		}
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		root["hooks"] = hooks
	}
	entries, _ := hooks[UserPromptSubmit].([]any)
	handler := map[string]any{
		"type":          "command",
		"command":       command,
		"timeout":       timeout,
		"statusMessage": "Enhancing prompt with openPE",
	}
	for _, entry := range entries {
		group, _ := entry.(map[string]any)
		if group == nil {
			continue
		}
		groupHooks, _ := group["hooks"].([]any)
		for _, item := range groupHooks {
			hook, _ := item.(map[string]any)
			if hook == nil {
				continue
			}
			existingCommand, _ := hook["command"].(string)
			if IsOpenPEHookCommand(existingCommand) {
				hook["type"] = "command"
				hook["command"] = command
				hook["timeout"] = timeout
				hook["statusMessage"] = "Enhancing prompt with openPE"
				return marshalHooksConfig(root)
			}
		}
	}
	entries = append(entries, map[string]any{
		"hooks": []any{handler},
	})
	hooks[UserPromptSubmit] = entries
	return marshalHooksConfig(root)
}

func IsOpenPEHookCommand(command string) bool {
	return strings.Contains(command, "openpe") && strings.Contains(command, "codex hook run")
}

func ProjectHooksPath(cwd string) string {
	return filepath.Join(cwd, ".codex", "hooks.json")
}

func ProjectEnvFile(cwd string) string {
	return filepath.Join(cwd, ".env")
}

func marshalHooksConfig(root map[string]any) ([]byte, error) {
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
