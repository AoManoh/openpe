package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	claudeadapter "github.com/AoManoh/openpe/internal/adapters/claude"
	codexadapter "github.com/AoManoh/openpe/internal/adapters/codex"
	devinadapter "github.com/AoManoh/openpe/internal/adapters/devin"
	windsurfadapter "github.com/AoManoh/openpe/internal/adapters/windsurf"
	"github.com/AoManoh/openpe/internal/fsatomic"
)

func hookDeadlineForTimeout(timeoutSeconds int) time.Duration {
	total := time.Duration(timeoutSeconds) * time.Second
	margin := 5 * time.Second
	if total <= margin {
		return total * 4 / 5
	}
	return total - margin
}

func withHookDeadlineArg(command string, timeoutSeconds int) string {
	return command + " --deadline=" + hookDeadlineForTimeout(timeoutSeconds).String()
}

func runCodexHookInstall(args []string, stdout io.Writer, stderr io.Writer, getwd func() (string, error)) int {
	fs := flag.NewFlagSet("codex hook install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scope := fs.String("scope", "user", "hook scope: user or project")
	target := fs.String("path", "", "explicit hooks.json path")
	openpeBin := fs.String("openpe-bin", "", "openpe executable path; defaults to PATH lookup")
	envFile := fs.String("env-file", "", "dotenv file loaded by the hook; defaults to project .env")
	hookTimeout := fs.Int("hook-timeout", 120, "Codex hook timeout in seconds")
	dryRun := fs.Bool("dry-run", false, "print hooks.json without writing it")
	if ok, code := parseFlagSet(fs, args); !ok {
		return code
	}
	if *hookTimeout <= 0 {
		fmt.Fprintln(stderr, "hook-timeout must be positive")
		return 1
	}
	hooksPath, err := codexHooksPath(*scope, *target, getwd)
	if err != nil {
		fmt.Fprintf(stderr, "resolve hooks path: %v\n", err)
		return 1
	}
	bin, err := resolveOpenPEBin(*openpeBin)
	if err != nil {
		fmt.Fprintf(stderr, "resolve openpe binary: %v\n", err)
		return 1
	}
	hookEnvFile, err := codexHookEnvFile(*scope, *target, *envFile, getwd)
	if err != nil {
		fmt.Fprintf(stderr, "resolve hook env file: %v\n", err)
		return 1
	}
	command := withHookDeadlineArg(codexadapter.HookCommandForScope(bin, hookEnvFile, *scope), *hookTimeout)
	merge := func(existing []byte) ([]byte, error) {
		return codexadapter.MergeHooksConfig(existing, command, *hookTimeout)
	}
	if *dryRun {
		return printHookConfigDryRun(hooksPath, merge, stdout, stderr)
	}
	changed, err := writeHookConfig(hooksPath, merge)
	if err != nil {
		fmt.Fprintf(stderr, "write hooks config: %v\n", err)
		return 1
	}
	if changed {
		fmt.Fprintf(stdout, "installed openPE Codex hook: %s\n", hooksPath)
	} else {
		fmt.Fprintf(stdout, "openPE Codex hook already installed: %s\n", hooksPath)
	}
	return 0
}

func runClaudeHookInstall(args []string, stdout io.Writer, stderr io.Writer, getwd func() (string, error)) int {
	fs := flag.NewFlagSet("claude hook install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	target := fs.String("path", "", "explicit Claude settings.json path")
	openpeBin := fs.String("openpe-bin", "", "openpe executable path; defaults to PATH lookup")
	envFile := fs.String("env-file", "", "dotenv file loaded by the hook; defaults to ~/.config/openpe/.env")
	hookTimeout := fs.Int("hook-timeout", 120, "Claude hook timeout in seconds")
	dryRun := fs.Bool("dry-run", false, "print settings.json without writing it")
	if ok, code := parseFlagSet(fs, args); !ok {
		return code
	}
	if *hookTimeout <= 0 {
		fmt.Fprintln(stderr, "hook-timeout must be positive")
		return 1
	}
	settingsPath, err := claudeSettingsPath(*target)
	if err != nil {
		fmt.Fprintf(stderr, "resolve Claude settings path: %v\n", err)
		return 1
	}
	bin, err := resolveOpenPEBin(*openpeBin)
	if err != nil {
		fmt.Fprintf(stderr, "resolve openpe binary: %v\n", err)
		return 1
	}
	hookEnvFile, err := claudeHookEnvFile(*envFile)
	if err != nil {
		fmt.Fprintf(stderr, "resolve hook env file: %v\n", err)
		return 1
	}
	command := withHookDeadlineArg(claudeadapter.HookCommand(bin, hookEnvFile), *hookTimeout)
	merge := func(existing []byte) ([]byte, error) {
		return claudeadapter.MergeSettings(existing, command, *hookTimeout)
	}
	if *dryRun {
		return printHookConfigDryRun(settingsPath, merge, stdout, stderr)
	}
	changed, err := writeHookConfig(settingsPath, merge)
	if err != nil {
		fmt.Fprintf(stderr, "write Claude settings: %v\n", err)
		return 1
	}
	if changed {
		fmt.Fprintf(stdout, "installed openPE Claude hook: %s\n", settingsPath)
	} else {
		fmt.Fprintf(stdout, "openPE Claude hook already installed: %s\n", settingsPath)
	}
	return 0
}

func runWindsurfHookInstall(args []string, stdout io.Writer, stderr io.Writer, getwd func() (string, error)) int {
	fs := flag.NewFlagSet("windsurf hook install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scope := fs.String("scope", "user", "hook scope: user or project")
	target := fs.String("path", "", "explicit Windsurf hooks.json path")
	openpeBin := fs.String("openpe-bin", "", "openpe executable path; defaults to PATH lookup")
	envFile := fs.String("env-file", "", "dotenv file loaded by the hook; defaults to ~/.config/openpe/.env for user hooks or project .env for project hooks")
	hookTimeout := fs.Int("hook-timeout", 120, "host hook timeout in seconds; derives the openPE self-deadline")
	dryRun := fs.Bool("dry-run", false, "print hooks.json without writing it")
	if ok, code := parseFlagSet(fs, args); !ok {
		return code
	}
	if *hookTimeout <= 0 {
		fmt.Fprintln(stderr, "hook-timeout must be positive")
		return 1
	}
	hooksPath, err := windsurfHooksPath(*scope, *target, getwd)
	if err != nil {
		fmt.Fprintf(stderr, "resolve Windsurf hooks path: %v\n", err)
		return 1
	}
	bin, err := resolveOpenPEBin(*openpeBin)
	if err != nil {
		fmt.Fprintf(stderr, "resolve openpe binary: %v\n", err)
		return 1
	}
	hookEnvFile, err := windsurfHookEnvFile(*scope, *target, *envFile, getwd)
	if err != nil {
		fmt.Fprintf(stderr, "resolve hook env file: %v\n", err)
		return 1
	}
	command := withHookDeadlineArg(windsurfadapter.HookCommand(bin, hookEnvFile), *hookTimeout)
	powershell := withHookDeadlineArg(windsurfadapter.PowerShellHookCommand(bin, hookEnvFile), *hookTimeout)
	merge := func(existing []byte) ([]byte, error) {
		return windsurfadapter.MergeHooksConfig(existing, command, powershell)
	}
	if *dryRun {
		return printHookConfigDryRun(hooksPath, merge, stdout, stderr)
	}
	changed, err := writeHookConfig(hooksPath, merge)
	if err != nil {
		fmt.Fprintf(stderr, "write Windsurf hooks config: %v\n", err)
		return 1
	}
	if changed {
		fmt.Fprintf(stdout, "installed openPE Windsurf hook: %s\n", hooksPath)
	} else {
		fmt.Fprintf(stdout, "openPE Windsurf hook already installed: %s\n", hooksPath)
	}
	return 0
}

func runDevinHookInstall(args []string, stdout io.Writer, stderr io.Writer, getwd func() (string, error)) int {
	fs := flag.NewFlagSet("devin hook install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scope := fs.String("scope", "user", "hook scope: user or project")
	target := fs.String("path", "", "explicit hooks file path (.devin/hooks.v1.json or a devin config.json)")
	openpeBin := fs.String("openpe-bin", "", "openpe executable path; defaults to PATH lookup")
	envFile := fs.String("env-file", "", "dotenv file loaded by the hook; defaults to ~/.config/openpe/.env for user hooks or project .env for project hooks")
	hookTimeout := fs.Int("hook-timeout", 120, "Devin hook timeout in seconds")
	dryRun := fs.Bool("dry-run", false, "print the merged config without writing it")
	if ok, code := parseFlagSet(fs, args); !ok {
		return code
	}
	if *hookTimeout <= 0 {
		fmt.Fprintln(stderr, "hook-timeout must be positive")
		return 1
	}
	hooksPath, err := devinHooksPath(*scope, *target, getwd)
	if err != nil {
		fmt.Fprintf(stderr, "resolve devin hooks path: %v\n", err)
		return 1
	}
	bin, err := resolveOpenPEBin(*openpeBin)
	if err != nil {
		fmt.Fprintf(stderr, "resolve openpe binary: %v\n", err)
		return 1
	}
	hookEnvFile, err := devinHookEnvFile(*scope, *target, *envFile, getwd)
	if err != nil {
		fmt.Fprintf(stderr, "resolve hook env file: %v\n", err)
		return 1
	}
	command := withHookDeadlineArg(devinadapter.HookCommandForScope(bin, hookEnvFile, *scope), *hookTimeout)
	merge := func(existing []byte) ([]byte, error) {
		if devinConfigIsWrapped(hooksPath) {
			// ~/.config/devin/config.json (or config.local.json): hooks live
			// under the top-level "hooks" key; preserve all other config.
			return devinadapter.MergeConfigHooks(existing, command, *hookTimeout)
		}
		// .devin/hooks.v1.json: the hooks object is the entire file.
		return devinadapter.MergeStandaloneHooks(existing, command, *hookTimeout)
	}
	if *dryRun {
		return printHookConfigDryRun(hooksPath, merge, stdout, stderr)
	}
	changed, err := writeHookConfig(hooksPath, merge)
	if err != nil {
		fmt.Fprintf(stderr, "write devin hooks config: %v\n", err)
		return 1
	}
	if changed {
		fmt.Fprintf(stdout, "installed openPE Devin hook: %s\n", hooksPath)
	} else {
		fmt.Fprintf(stdout, "openPE Devin hook already installed: %s\n", hooksPath)
	}
	return 0
}

// printHookConfigDryRun renders the merge result without writing, reading the
// config outside the lock (a dry run must never contend with real installs).
func printHookConfigDryRun(path string, merge func([]byte) ([]byte, error), stdout io.Writer, stderr io.Writer) int {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(stderr, "read config: %v\n", err)
		return 1
	}
	merged, err := merge(existing)
	if err != nil {
		fmt.Fprintf(stderr, "merge config: %v\n", err)
		return 1
	}
	_, _ = stdout.Write(merged)
	return 0
}

func devinHooksPath(scope string, target string, getwd func() (string, error)) (string, error) {
	if strings.TrimSpace(target) != "" {
		return filepath.Clean(target), nil
	}
	switch scope {
	case "project":
		cwd, err := getwd()
		if err != nil {
			return "", err
		}
		return devinadapter.ProjectHooksPath(cwd), nil
	case "user":
		path := devinadapter.UserConfigPath()
		if path == "" {
			return "", fmt.Errorf("resolve user home directory")
		}
		return path, nil
	default:
		return "", fmt.Errorf("unsupported scope %q", scope)
	}
}

// devinConfigIsWrapped reports whether the target path is a Devin config file
// (hooks nested under "hooks") rather than a standalone .devin/hooks.v1.json
// (hooks object is the whole file).
func devinConfigIsWrapped(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return base == "config.json" || base == "config.local.json"
}

func devinHookEnvFile(scope string, target string, value string, getwd func() (string, error)) (string, error) {
	value = strings.TrimSpace(value)
	if value != "" {
		return filepath.Abs(value)
	}
	if scope == "user" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config", "openpe", ".env"), nil
	}
	if strings.TrimSpace(target) != "" {
		return "", nil
	}
	cwd, err := getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, ".env"), nil
}

func codexHooksPath(scope string, target string, getwd func() (string, error)) (string, error) {
	if strings.TrimSpace(target) != "" {
		return filepath.Clean(target), nil
	}
	switch scope {
	case "project":
		cwd, err := getwd()
		if err != nil {
			return "", err
		}
		return filepath.Join(cwd, ".codex", "hooks.json"), nil
	case "user":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".codex", "hooks.json"), nil
	default:
		return "", fmt.Errorf("unsupported scope %q", scope)
	}
}

func claudeSettingsPath(target string) (string, error) {
	if strings.TrimSpace(target) != "" {
		return filepath.Abs(target)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

func windsurfHooksPath(scope string, target string, getwd func() (string, error)) (string, error) {
	if strings.TrimSpace(target) != "" {
		return filepath.Clean(target), nil
	}
	switch scope {
	case "project":
		cwd, err := getwd()
		if err != nil {
			return "", err
		}
		return windsurfadapter.ProjectHooksPath(cwd), nil
	case "user":
		path := windsurfadapter.UserHooksPath()
		if path == "" {
			return "", fmt.Errorf("resolve user home directory")
		}
		return path, nil
	default:
		return "", fmt.Errorf("unsupported scope %q", scope)
	}
}

func resolveOpenPEBin(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value != "" {
		if strings.ContainsAny(value, `/\`) {
			return filepath.Abs(value)
		}
		if found, err := exec.LookPath(value); err == nil {
			return filepath.Abs(found)
		}
		return filepath.Abs(value)
	}
	if found, err := exec.LookPath("openpe"); err == nil {
		return filepath.Abs(found)
	}
	return os.Executable()
}

func codexHookEnvFile(scope string, target string, value string, getwd func() (string, error)) (string, error) {
	value = strings.TrimSpace(value)
	if value != "" {
		return filepath.Abs(value)
	}
	if scope == "user" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config", "openpe", ".env"), nil
	}
	if strings.TrimSpace(target) != "" {
		return "", nil
	}
	cwd, err := getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, ".env"), nil
}

func claudeHookEnvFile(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value != "" {
		return filepath.Abs(value)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "openpe", ".env"), nil
}

func windsurfHookEnvFile(scope string, target string, value string, getwd func() (string, error)) (string, error) {
	value = strings.TrimSpace(value)
	if value != "" {
		return filepath.Abs(value)
	}
	if scope == "user" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".config", "openpe", ".env"), nil
	}
	if strings.TrimSpace(target) != "" {
		return "", nil
	}
	cwd, err := getwd()
	if err != nil {
		return "", err
	}
	return windsurfadapter.ProjectEnvFile(cwd), nil
}

// writeHookConfig applies merge to a user-owned hook config file under the
// file's cross-process lock: read the CURRENT content, merge, and replace
// atomically, so two concurrent installers serialize instead of losing one
// side's update, and a crash or full disk can no longer truncate the user's
// config. Symlinked configs are replaced through their target (fsatomic).
// Returns whether the file content actually changed.
func writeHookConfig(path string, merge func(existing []byte) ([]byte, error)) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("create config directory: %w", err)
	}
	unlock, err := fsatomic.Lock(path)
	if err != nil {
		return false, fmt.Errorf("lock config: %w", err)
	}
	defer unlock()
	for attempt := 0; attempt < 3; attempt++ {
		existing, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return false, fmt.Errorf("read config: %w", err)
		}
		if os.IsNotExist(err) {
			existing = nil
		}
		merged, err := merge(existing)
		if err != nil {
			return false, err
		}
		if bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace(merged)) {
			return false, nil
		}
		if err := fsatomic.ReplaceGuarded(path, existing, merged, 0o644); err == nil {
			return true, nil
		} else if attempt == 2 {
			return false, fmt.Errorf("config kept changing while merging: %w", err)
		}
	}
	return false, fmt.Errorf("config kept changing while merging")
}
