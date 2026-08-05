// Package fsatomic provides atomic file replacement and cross-process
// locking for state that multiple processes read, write, and clean up
// concurrently: delivery caches, user hook configs, server descriptors.
//
// A plain os.WriteFile truncates in place, so a crash, a full disk, or a
// concurrent reader can observe a half-written file. All writes here go
// through a same-directory temp file + fsync + rename, so any reader
// observes either the previous or the new content, never a mix. Read-check-
// act sequences that content comparison cannot protect (publish vs
// ownership-checked cleanup, read-merge-write config updates) serialize via
// Lock's sidecar advisory lock instead.
package fsatomic

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// WriteFile atomically replaces path with data. The temp file lives in the
// destination directory so the final rename never crosses filesystems. When
// the destination already exists its permission bits are preserved;
// otherwise perm applies.
//
// A symlinked destination is resolved first and the TARGET is replaced: a
// plain rename would swap the symlink itself for a regular file, silently
// detaching dotfiles-managed configs from their repository (the pre-atomic
// os.WriteFile used to write through the link, so following it preserves
// that contract).
func WriteFile(path string, data []byte, perm os.FileMode) error {
	return writeFilePrepared(path, data, perm, nil, true, nil, false)
}

// WriteFilePrepared 与 WriteFile 相同，但在 temp 可见性提交前调用 prepare。
// descriptor 用它先设置 Windows owner-only DACL，再 rename；这样不会出现
// “宽继承 ACL 文件已发布、随后才收紧”的泄漏窗口。
func WriteFilePrepared(path string, data []byte, perm os.FileMode, prepare func(string) error) error {
	return writeFilePrepared(path, data, perm, prepare, true, nil, false)
}

// WriteFilePreparedExact 不继承目标文件旧权限，专供含 secret 的状态文件。
func WriteFilePreparedExact(path string, data []byte, perm os.FileMode, prepare func(string) error) error {
	return writeFilePrepared(path, data, perm, prepare, false, nil, false)
}

// ReplaceGuarded 在最终 rename 前再次比较当前内容。它与合作 writer 的
// sidecar lock 配合；对不获取 lock 的宿主/编辑器，至少检测并拒绝已发生的
// 修改，而不是静默覆盖。expected=nil 表示读取时文件不存在。
func ReplaceGuarded(path string, expected []byte, data []byte, perm os.FileMode) error {
	resolved, err := ResolvePath(path)
	if err != nil {
		return err
	}
	current, err := os.ReadFile(resolved)
	switch {
	case err == nil && expected == nil:
		return fmt.Errorf("fsatomic: guarded target appeared: %s", resolved)
	case err == nil && !bytes.Equal(current, expected):
		return fmt.Errorf("fsatomic: guarded target changed: %s", resolved)
	case os.IsNotExist(err) && expected != nil:
		return fmt.Errorf("fsatomic: guarded target disappeared: %s", resolved)
	case err != nil && !os.IsNotExist(err):
		return fmt.Errorf("fsatomic: read guarded target: %w", err)
	}
	return writeFilePrepared(resolved, data, perm, nil, true, expected, true)
}

func writeFilePrepared(path string, data []byte, perm os.FileMode, prepare func(string) error, preservePerm bool, expected []byte, guarded bool) error {
	if path == "" {
		return errors.New("fsatomic: path is required")
	}
	resolved, err := resolvePath(path)
	if err != nil {
		return err
	}
	path = resolved
	if preservePerm {
		if info, err := os.Stat(path); err == nil {
			perm = info.Mode().Perm()
		}
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("fsatomic: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("fsatomic: chmod temp file: %w", err)
	}
	if prepare != nil {
		if err := prepare(tmpName); err != nil {
			_ = tmp.Close()
			cleanup()
			return fmt.Errorf("fsatomic: prepare temp file: %w", err)
		}
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("fsatomic: write temp file: %w", err)
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
	if guarded {
		current, err := os.ReadFile(path)
		switch {
		case err == nil && expected == nil:
			cleanup()
			return fmt.Errorf("fsatomic: guarded target appeared: %s", path)
		case err == nil && !bytes.Equal(current, expected):
			cleanup()
			return fmt.Errorf("fsatomic: guarded target changed: %s", path)
		case os.IsNotExist(err) && expected != nil:
			cleanup()
			return fmt.Errorf("fsatomic: guarded target disappeared: %s", path)
		case err != nil && !os.IsNotExist(err):
			cleanup()
			return fmt.Errorf("fsatomic: read guarded target before rename: %w", err)
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("fsatomic: rename temp file: %w", err)
	}
	syncDir(dir)
	return nil
}

// ResolvePath resolves a symlinked destination (and, for a not-yet-existing
// destination, its symlinked parent). Callers that lock before a read/merge/
// write transaction must lock this resolved path, otherwise two aliases to
// the same target would use different sidecar locks.
func ResolvePath(path string) (string, error) {
	if path == "" {
		return "", errors.New("fsatomic: path is required")
	}
	return resolvePath(path)
}

func resolvePath(path string) (string, error) {
	current := filepath.Clean(path)
	for depth := 0; depth < 32; depth++ {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink == 0 {
				resolved, err := filepath.EvalSymlinks(current)
				if err != nil {
					return "", fmt.Errorf("fsatomic: resolve destination: %w", err)
				}
				return resolved, nil
			}
			target, err := os.Readlink(current)
			if err != nil {
				return "", fmt.Errorf("fsatomic: read destination symlink: %w", err)
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(current), target)
			}
			current = filepath.Clean(target)
			continue
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("fsatomic: inspect destination: %w", err)
		}
		return resolveMissingPath(current)
	}
	return "", fmt.Errorf("fsatomic: too many destination symlinks: %s", path)
}

func resolveMissingPath(path string) (string, error) {
	var suffix []string
	ancestor := filepath.Clean(path)
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			resolved, err := filepath.EvalSymlinks(ancestor)
			if err != nil {
				return "", fmt.Errorf("fsatomic: resolve destination ancestor: %w", err)
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("fsatomic: inspect destination ancestor: %w", err)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return path, nil
		}
		suffix = append(suffix, filepath.Base(ancestor))
		ancestor = parent
	}
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
