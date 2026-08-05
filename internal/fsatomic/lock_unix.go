//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly || solaris

package fsatomic

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockHandle takes a blocking exclusive flock on the open file. flock locks
// are per-open-file-description, so two opens contend even within a single
// process — which is exactly what the tests exercise.
func lockHandle(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX)
}

func unlockHandle(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
