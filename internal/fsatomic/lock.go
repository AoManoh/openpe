package fsatomic

import (
	"fmt"
	"os"
	"path/filepath"
)

// Unlocker releases a cross-process lock acquired by Lock.
type Unlocker func()

// Lock acquires an exclusive, blocking, cross-process advisory lock on a
// sidecar file derived from path (path + ".lock"). It serializes writers
// AND cleaners of multi-process state files — the descriptor publish /
// ownership-check-remove sequence, and the hook-config read-merge-write
// cycle — whose read-check-act windows cannot be closed by content
// comparison alone.
//
// The sidecar file is created if missing and intentionally never deleted:
// removing a lock file while another process holds or is about to open it
// reintroduces the very race the lock exists to prevent. The empty sidecar
// is a few bytes of permanent state next to the file it guards.
func Lock(path string) (Unlocker, error) {
	if path == "" {
		return nil, fmt.Errorf("fsatomic: lock path is required")
	}
	resolved, err := ResolvePath(path)
	if err != nil {
		return nil, err
	}
	lockPath := resolved + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("fsatomic: create lock directory: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("fsatomic: open lock file: %w", err)
	}
	if err := lockHandle(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("fsatomic: acquire lock: %w", err)
	}
	return func() {
		_ = unlockHandle(f)
		_ = f.Close()
	}, nil
}
