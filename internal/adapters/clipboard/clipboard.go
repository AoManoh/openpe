package clipboard

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"
)

const defaultTimeout = 2 * time.Second
const osc52MaxBytes = 100 * 1024

// pipeWaitDelay bounds how long a finished (or context-killed) clipboard
// helper may keep us in Wait through inherited output pipes. xclip in
// particular forks a child that owns the X selection and inherits
// stdout/stderr; without a WaitDelay, Wait blocks until that child exits —
// on 2026-08-03 this held the Devin hook for ~115s until the host killed it
// and the raw `pe` prompt sailed through to the model.
const pipeWaitDelay = 500 * time.Millisecond

type commandSpec struct {
	name         string
	args         []string
	stdinUTF16LE bool
}

type Options struct {
	Command      string
	DisableOSC52 bool
	OSC52TTY     string
}

func Copy(ctx context.Context, text string) (string, error) {
	return CopyWithOptions(ctx, text, Options{
		Command:      os.Getenv("OPENPE_COPY_COMMAND"),
		DisableOSC52: strings.TrimSpace(os.Getenv("OPENPE_DISABLE_OSC52_CLIPBOARD")) != "",
		OSC52TTY:     os.Getenv("OPENPE_OSC52_TTY"),
	})
}

func CopyWithOptions(ctx context.Context, text string, opts Options) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("empty clipboard text")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	if command := strings.TrimSpace(opts.Command); command != "" {
		if err := runShellCommand(ctx, command, text); err != nil {
			return "", err
		}
		return "OPENPE_COPY_COMMAND", nil
	}

	var attempted []string
	for _, spec := range candidates() {
		if _, err := exec.LookPath(spec.name); err != nil {
			continue
		}
		attempted = append(attempted, strings.Join(append([]string{spec.name}, spec.args...), " "))
		if err := runCommand(ctx, spec, text); err != nil {
			continue
		}
		return spec.name, nil
	}
	if len(attempted) == 0 {
		if err := copyOSC52(text, opts); err != nil {
			return "", fmt.Errorf("no supported clipboard command found; OSC 52 fallback failed: %w", err)
		}
		return "OSC52", nil
	}
	if err := copyOSC52(text, opts); err == nil {
		return "OSC52", nil
	} else {
		return "", fmt.Errorf("clipboard commands failed: %s; OSC 52 fallback failed: %w", strings.Join(attempted, ", "), err)
	}
}

func runShellCommand(ctx context.Context, command string, text string) error {
	shell, args := shellCommand(command)
	cmd := exec.CommandContext(ctx, shell, args...)
	cmd.Stdin = bytes.NewReader(commandInput(commandUsesClipExe(command), text))
	return runBounded(cmd)
}

func shellCommand(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe", []string{"/C", command}
	}
	return "sh", []string{"-c", command}
}

func runCommand(ctx context.Context, spec commandSpec, text string) error {
	cmd := exec.CommandContext(ctx, spec.name, spec.args...)
	cmd.Stdin = bytes.NewReader(commandInput(spec.stdinUTF16LE, text))
	return runBounded(cmd)
}

// runBounded runs a clipboard helper without hanging on inherited pipes.
// WaitDelay lets Wait return shortly after the direct child exits even when a
// forked descendant (xclip's selection owner) keeps stdout/stderr open; in
// that case the run counts as a successful copy: exec.ErrWaitDelay is only
// returned for processes that exited with a success status.
func runBounded(cmd *exec.Cmd) error {
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	cmd.WaitDelay = pipeWaitDelay
	err := cmd.Run()
	if err == nil || errors.Is(err, exec.ErrWaitDelay) {
		return nil
	}
	return fmt.Errorf("%s: %s", err, strings.TrimSpace(output.String()))
}

func copyOSC52(text string, opts Options) error {
	if opts.DisableOSC52 {
		return errors.New("OSC 52 clipboard fallback disabled")
	}
	if len([]byte(text)) > osc52MaxBytes {
		return fmt.Errorf("text exceeds OSC 52 safety limit of %d bytes", osc52MaxBytes)
	}
	ttyPath := strings.TrimSpace(opts.OSC52TTY)
	if ttyPath == "" {
		ttyPath = "/dev/tty"
	}
	tty, err := os.OpenFile(ttyPath, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer tty.Close()
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	_, err = fmt.Fprintf(tty, "\x1b]52;c;%s\a", encoded)
	return err
}

func candidates() []commandSpec {
	switch runtime.GOOS {
	case "darwin":
		return []commandSpec{{name: "pbcopy"}}
	case "windows":
		return []commandSpec{{name: "clip.exe", stdinUTF16LE: true}}
	default:
		specs := []commandSpec{
			{name: "wl-copy"},
			{name: "xclip", args: []string{"-selection", "clipboard"}},
			{name: "xsel", args: []string{"--clipboard", "--input"}},
		}
		if isWSL() {
			specs = append(specs, commandSpec{name: "clip.exe", stdinUTF16LE: true})
		}
		return specs
	}
}

func commandInput(stdinUTF16LE bool, text string) []byte {
	if stdinUTF16LE {
		return utf16LEWithBOM(text)
	}
	return []byte(text)
}

func commandUsesClipExe(command string) bool {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return false
	}
	return isClipExeName(fields[0])
}

func isClipExeName(name string) bool {
	name = strings.Trim(strings.TrimSpace(name), `"'`)
	name = strings.TrimRight(name, `/\`)
	if name == "" {
		return false
	}
	if idx := strings.LastIndexAny(name, `/\`); idx >= 0 {
		name = name[idx+1:]
	}
	return strings.EqualFold(name, "clip.exe")
}

func utf16LEWithBOM(text string) []byte {
	encoded := utf16.Encode([]rune(text))
	out := make([]byte, 2+len(encoded)*2)
	out[0] = 0xff
	out[1] = 0xfe
	for i, value := range encoded {
		binary.LittleEndian.PutUint16(out[2+i*2:], value)
	}
	return out
}

func isWSL() bool {
	if os.Getenv("WSL_INTEROP") != "" || os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	version := strings.ToLower(string(data))
	return strings.Contains(version, "microsoft") || strings.Contains(version, "wsl")
}
