//go:build !windows && !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly && !solaris

package fsatomic

import (
	"fmt"
	"os"
)

func lockHandle(f *os.File) error {
	return fmt.Errorf("cross-process file locking is unsupported on this platform")
}

func unlockHandle(f *os.File) error {
	return nil
}
