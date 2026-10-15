//go:build !unix && !windows

package privatefs

import (
	"fmt"
	"os"
)

func validateOwner(_ *os.File, info os.FileInfo) error {
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%w: private custody must not grant group/world write access", ErrUnsafeCustody)
	}
	return nil
}

func initializeDirectory(*os.File, os.FileInfo) error { return nil }

func protectFile(file *os.File, mode os.FileMode) error { return file.Chmod(mode) }

func validateFile(_ *os.File, info os.FileInfo) error {
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: private custody leaf must be an owner-only regular file", ErrUnsafeCustody)
	}
	return nil
}
