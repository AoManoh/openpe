//go:build windows

package fsatomic

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockHandle takes a blocking exclusive LockFileEx byte-range lock covering
// the whole (empty) sidecar file — the Windows analogue of flock.
func lockHandle(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0,
		1, 0,
		ol,
	)
}

func unlockHandle(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
}
