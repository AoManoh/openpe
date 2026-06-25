package devin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// HookCommand builds the user-scope hook command (no scope suffix).
func HookCommand(bin string, envFile string) string {
	return HookCommandForScope(bin, envFile, "")
}

// HookCommandForScope builds the shell command Devin runs for the
// UserPromptSubmit hook. It mirrors the codex adapter's runtime flags: block
// via stderr (exit 2), no /dev/tty preview, and copy the enhanced prompt to the
// clipboard so the user pastes it back.
func HookCommandForScope(bin string, envFile string, scope string) string {
	command := shellQuote(bin) + " devin hook run --block-output=stderr --terminal-preview=false --copy-preview=true"
	if strings.TrimSpace(scope) != "" {
		command += " --hook-scope=" + shellQuote(scope)
	}
	if strings.TrimSpace(envFile) == "" {
		return command
	}
	return "OPENPE_ENV_FILE=" + shellQuote(envFile) + " " + command
}

// MergeStandaloneHooks merges the openPE hook into a `.devin/hooks.v1.json`
// file, where the hooks object IS the entire file (no "hooks" wrapper key).
func MergeStandaloneHooks(existing []byte, command string, timeout int) ([]byte, error) {
	root := map[string]any{}
	if len(strings.TrimSpace(string(existing))) > 0 {
		if err := json.Unmarshal(existing, &root); err != nil {
			return nil, err
		}
	}
	mergeUserPromptSubmit(root, command, timeout)
	return marshalConfig(root)
}

// MergeConfigHooks merges the openPE hook into a Devin config file
// (`~/.config/devin/config.json` or `.devin/config.json`), where hooks live
// under the top-level "hooks" key. All other config keys are preserved.
func MergeConfigHooks(existing []byte, command string, timeout int) ([]byte, error) {
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
	mergeUserPromptSubmit(hooks, command, timeout)
	return marshalConfig(root)
}

// mergeUserPromptSubmit upserts the openPE handler into hooks[UserPromptSubmit]
// idempotently: an existing openPE handler is updated in place (and duplicates
// dropped); foreign hooks are preserved untouched. Operates in place on the
// passed hooks map.
func mergeUserPromptSubmit(hooks map[string]any, command string, timeout int) {
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
}

// IsOpenPEHookCommand reports whether a hook command string is openPE's Devin
// hook, used for idempotent install and duplicate detection.
func IsOpenPEHookCommand(command string) bool {
	return strings.Contains(command, "openpe") && strings.Contains(command, "devin hook run")
}

// ProjectHooksPath is the project-scope standalone hooks file.
func ProjectHooksPath(cwd string) string {
	return filepath.Join(cwd, ".devin", "hooks.v1.json")
}

// ProjectEnvFile is the default project-scope dotenv file.
func ProjectEnvFile(cwd string) string {
	return filepath.Join(cwd, ".env")
}

// UserConfigPath is the user-scope Devin config (hooks under "hooks").
func UserConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "devin", "config.json")
}

// HasUserOpenPEHookConfig reports whether the user-scope Devin config already
// contains an openPE hook (used to skip redundant project installs).
func HasUserOpenPEHookConfig() bool {
	path := UserConfigPath()
	if path == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	root := map[string]any{}
	if err := json.Unmarshal(data, &root); err != nil {
		return false
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		return false
	}
	return hooksContainOpenPE(hooks)
}

func hooksContainOpenPE(hooks map[string]any) bool {
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

func marshalConfig(root map[string]any) ([]byte, error) {
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
