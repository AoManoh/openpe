package clipboard

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const defaultTimeout = 2 * time.Second
const osc52MaxBytes = 100 * 1024

type commandSpec struct {
	name string
	args []string
}

func Copy(ctx context.Context, text string) (string, error) {
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

	if command := strings.TrimSpace(os.Getenv("OPENPE_COPY_COMMAND")); command != "" {
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
		if err := copyOSC52(text); err != nil {
			return "", fmt.Errorf("no supported clipboard command found; OSC 52 fallback failed: %w", err)
		}
		return "OSC52", nil
	}
	if err := copyOSC52(text); err == nil {
		return "OSC52", nil
	} else {
		return "", fmt.Errorf("clipboard commands failed: %s; OSC 52 fallback failed: %w", strings.Join(attempted, ", "), err)
	}
}

func runShellCommand(ctx context.Context, command string, text string) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdin = strings.NewReader(text)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func runCommand(ctx context.Context, spec commandSpec, text string) error {
	cmd := exec.CommandContext(ctx, spec.name, spec.args...)
	cmd.Stdin = strings.NewReader(text)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func copyOSC52(text string) error {
	if strings.TrimSpace(os.Getenv("OPENPE_DISABLE_OSC52_CLIPBOARD")) != "" {
		return errors.New("OSC 52 clipboard fallback disabled")
	}
	if len([]byte(text)) > osc52MaxBytes {
		return fmt.Errorf("text exceeds OSC 52 safety limit of %d bytes", osc52MaxBytes)
	}
	ttyPath := strings.TrimSpace(os.Getenv("OPENPE_OSC52_TTY"))
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
		return []commandSpec{{name: "clip.exe"}}
	default:
		return []commandSpec{
			{name: "wl-copy"},
			{name: "xclip", args: []string{"-selection", "clipboard"}},
			{name: "xsel", args: []string{"--clipboard", "--input"}},
		}
	}
}
