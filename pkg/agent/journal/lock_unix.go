//go:build !windows

package journal

import (
	"errors"
	"os"
	"syscall"

	"github.com/kombifyio/techstack/internal/privatefs"
)

func acquireLock(root *os.Root, path string) (*os.File, error) {
	file, err := privatefs.OpenFile(root, path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return file, nil
}

func releaseLock(file *os.File) error {
	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return errors.Join(unlockErr, file.Close())
}

func syncDir(root *os.Root) error {
	handle, err := root.Open(".")
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

func freeBytes(root *os.Root) (uint64, error) {
	file, err := root.Open(".")
	if err != nil {
		return 0, err
	}
	defer file.Close()
	var st syscall.Statfs_t
	if err := syscall.Fstatfs(int(file.Fd()), &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil // #nosec G115 -- filesystem block counts are non-negative.
}

func isNoSpace(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}
