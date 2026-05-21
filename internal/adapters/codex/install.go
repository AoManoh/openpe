package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

func HookCommand(bin string, envFile string) string {
	return HookCommandForScope(bin, envFile, "")
}

func HookCommandForScope(bin string, envFile string, scope string) string {
	command := shellQuote(bin) + " codex hook run --block-output=stderr --terminal-preview=false --copy-preview=true"
	if strings.TrimSpace(scope) != "" {
		command += " --hook-scope=" + shellQuote(scope)
	}
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
				hook["statusMessage"] = "Enhancing prompt with openPE"
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
			"hooks": []any{handler},
		})
	}
	hooks[UserPromptSubmit] = normalizedEntries
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

func ShouldSkipDuplicateHook(scope string, envFile string, cwd string) bool {
	scope = strings.TrimSpace(scope)
	if scope == "user" || !HasUserOpenPEHookConfig() {
		return false
	}
	switch scope {
	case "project":
		return true
	case "":
		cwd = strings.TrimSpace(cwd)
		envFile = strings.TrimSpace(envFile)
		if cwd == "" || envFile == "" {
			return false
		}
		return isPathInside(cwd, envFile)
	default:
		return false
	}
}

func UserHooksPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "hooks.json")
}

func HasUserOpenPEHookConfig() bool {
	path := UserHooksPath()
	return path != "" && HasOpenPEHookConfig(path)
}

func HasOpenPEHookConfig(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return HooksConfigContainsOpenPEHook(data)
}

func HooksConfigContainsOpenPEHook(data []byte) bool {
	root := map[string]any{}
	if err := json.Unmarshal(data, &root); err != nil {
		return false
	}
	hooks, _ := root["hooks"].(map[string]any)
	entries, _ := hooks[UserPromptSubmit].([]any)
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
			command, _ := hook["command"].(string)
			if IsOpenPEHookCommand(command) {
				return true
			}
		}
	}
	return false
}

func isPathInside(base string, target string) bool {
	absBase, err := filepath.Abs(base)
	if err != nil {
		return false
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absBase, absTarget)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
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
