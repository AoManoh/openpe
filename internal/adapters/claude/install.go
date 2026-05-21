package claude

import (
	"encoding/json"
	"strings"
)

func HookCommand(bin string, envFile string) string {
	command := shellQuote(bin) + " claude hook run"
	if strings.TrimSpace(envFile) == "" {
		return command
	}
	return "OPENPE_ENV_FILE=" + shellQuote(envFile) + " " + command
}

func MergeSettings(existing []byte, command string, timeout int) ([]byte, error) {
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
		"type":    "command",
		"command": command,
		"timeout": timeout,
	}
	updated := false
	normalizedEntries := make([]any, 0, len(entries)+1)
	for _, entry := range entries {
		group, _ := entry.(map[string]any)
		if group == nil {
			normalizedEntries = append(normalizedEntries, entry)
			continue
		}
		groupHooks, _ := group["hooks"].([]any)
		normalizedHooks := make([]any, 0, len(groupHooks))
		for _, item := range groupHooks {
			hook, _ := item.(map[string]any)
			if hook == nil {
				normalizedHooks = append(normalizedHooks, item)
				continue
			}
			existingCommand, _ := hook["command"].(string)
			if IsOpenPEHookCommand(existingCommand) {
				if updated {
					continue
				}
				hook["type"] = "command"
				hook["command"] = command
				hook["timeout"] = timeout
				updated = true
			}
			normalizedHooks = append(normalizedHooks, item)
		}
		if len(normalizedHooks) == 0 {
			continue
		}
		group["hooks"] = normalizedHooks
		normalizedEntries = append(normalizedEntries, group)
	}
	if !updated {
		normalizedEntries = append(normalizedEntries, map[string]any{
			"matcher": "",
			"hooks":   []any{handler},
		})
	}
	hooks[UserPromptSubmit] = normalizedEntries
	return marshalSettings(root)
}

func IsOpenPEHookCommand(command string) bool {
	return strings.Contains(command, "openpe") && strings.Contains(command, "claude hook run")
}

func marshalSettings(root map[string]any) ([]byte, error) {
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
