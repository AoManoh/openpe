package windsurf

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

func HookCommand(bin string, envFile string) string {
	command := shellQuote(bin) + " windsurf hook run"
	if strings.TrimSpace(envFile) == "" {
		return command
	}
	return "OPENPE_ENV_FILE=" + shellQuote(envFile) + " " + command
}

func PowerShellHookCommand(bin string, envFile string) string {
	command := "& " + powerShellQuote(bin) + " windsurf hook run"
	if strings.TrimSpace(envFile) == "" {
		return command
	}
	return "$env:OPENPE_ENV_FILE=" + powerShellQuote(envFile) + "; " + command
}

func MergeHooksConfig(existing []byte, command string, powershell string) ([]byte, error) {
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
	entries, _ := hooks[PreUserPrompt].([]any)
	handler := hookHandler(command, powershell)
	updated := false
	normalizedEntries := make([]any, 0, len(entries)+1)
	for _, entry := range entries {
		hook, _ := entry.(map[string]any)
		if hook == nil {
			normalizedEntries = append(normalizedEntries, entry)
			continue
		}
		existingCommand, _ := hook["command"].(string)
		existingPowerShell, _ := hook["powershell"].(string)
		if IsOpenPEHookCommand(existingCommand) || IsOpenPEHookCommand(existingPowerShell) {
			if updated {
				continue
			}
			hook["command"] = command
			if strings.TrimSpace(powershell) != "" {
				hook["powershell"] = powershell
			} else {
				delete(hook, "powershell")
			}
			updated = true
		}
		normalizedEntries = append(normalizedEntries, hook)
	}
	if !updated {
		normalizedEntries = append(normalizedEntries, handler)
	}
	hooks[PreUserPrompt] = normalizedEntries
	return marshalHooksConfig(root)
}

func IsOpenPEHookCommand(command string) bool {
	return strings.Contains(command, "openpe") && strings.Contains(command, "windsurf hook run")
}

func ProjectHooksPath(cwd string) string {
	return filepath.Join(cwd, ".windsurf", "hooks.json")
}

func UserHooksPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codeium", "windsurf", "hooks.json")
}

func ProjectEnvFile(cwd string) string {
	return filepath.Join(cwd, ".env")
}

func hookHandler(command string, powershell string) map[string]any {
	handler := map[string]any{
		"command": command,
	}
	if strings.TrimSpace(powershell) != "" {
		handler["powershell"] = powershell
	}
	return handler
}

func marshalHooksConfig(root map[string]any) ([]byte, error) {
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

func powerShellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
