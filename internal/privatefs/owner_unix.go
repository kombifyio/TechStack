//go:build unix

package privatefs

import (
	"fmt"
	"os"
	"syscall"
)

func validateOwner(_ *os.File, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%w: private custody must be owned by this process user without group/world write access", ErrUnsafeCustody)
	}
	return nil
}

func initializeDirectory(*os.File, os.FileInfo) error { return nil }

func protectFile(file *os.File, mode os.FileMode) error { return file.Chmod(mode) }

func validateFile(_ *os.File, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: private custody leaf must be an owner-only, single-link regular file", ErrUnsafeCustody)
	}
	return nil
}
