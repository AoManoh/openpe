// Package fsatomic provides atomic file replacement for state the rest of the
// process (or another process) may read concurrently: delivery caches, user
// hook configs, descriptors. A plain os.WriteFile truncates in place, so a
// crash, a full disk, or a concurrent reader can observe a half-written file
// (CR-003 saw torn delivery caches, CR-013 truncated user hook configs).
//
// All writes go through a same-directory temp file + fsync + rename, so any
// reader observes either the previous or the new content, never a mix.
package fsatomic

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// ErrModified reports that a guarded replace found the file changed (or
// created/removed) since the caller read it. The caller must re-read,
// re-apply its change, and retry — blindly writing would drop the other
// writer's update (CR-013's lost-update shape).
var ErrModified = errors.New("fsatomic: file changed since it was read")

// WriteFile atomically replaces path with data. The temp file lives in the
// destination directory so the final rename never crosses filesystems. When
// the destination already exists its permission bits are preserved;
// otherwise perm applies.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	if path == "" {
		return errors.New("fsatomic: path is required")
	}
	if info, err := os.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("fsatomic: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("fsatomic: write temp file: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("fsatomic: chmod temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("fsatomic: sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("fsatomic: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("fsatomic: rename temp file: %w", err)
	}
	syncDir(dir)
	return nil
}

// ReplaceGuarded atomically replaces path with data, but only if the current
// content still equals expectedOld — the bytes the caller based its new
// content on. expectedOld == nil means "the caller saw no file"; a file that
// has appeared in the meantime is a conflict. On mismatch it returns
// ErrModified without touching the file.
//
// The read-compare-rename window is small but not zero: this is a lost-update
// guard for low-frequency writers (hook installers), not a general-purpose
// cross-process CAS.
func ReplaceGuarded(path string, expectedOld []byte, data []byte, perm os.FileMode) error {
	current, err := os.ReadFile(path)
	switch {
	case err == nil:
		if expectedOld == nil || !bytes.Equal(current, expectedOld) {
			return fmt.Errorf("%w: %s", ErrModified, path)
		}
	case os.IsNotExist(err):
		if expectedOld != nil {
			return fmt.Errorf("%w: %s (file removed)", ErrModified, path)
		}
	default:
		return fmt.Errorf("fsatomic: read current content: %w", err)
	}
	return WriteFile(path, data, perm)
}

// syncDir makes the rename durable where the platform supports it. Best
// effort: a failed directory sync does not undo an already-visible rename.
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
