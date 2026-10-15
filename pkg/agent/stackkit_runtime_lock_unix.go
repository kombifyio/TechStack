//go:build !windows

package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func checkStackKitRuntimeAncestor(root *os.Root, _ bool) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("public runtime ancestor must be owned by root or this process user without group/world write access")
	}
	return nil
}

func trustedStackKitTempAncestor(path string, info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return path == filepath.Clean(os.TempDir()) && info.Mode()&os.ModeSticky != 0 && ok && stat.Uid == 0
}

func tryStackKitRuntimeLock(file *os.File, exclusive bool) (bool, error) {
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	err := syscall.Flock(int(file.Fd()), mode|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}

func releaseStackKitRuntimeLock(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}
