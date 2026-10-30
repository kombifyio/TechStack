//go:build !windows

package localdb

import (
	"os"
	"syscall"
)

func lockProfile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
