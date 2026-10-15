//go:build unix

package guardlayout

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"github.com/kombifyio/techstack/internal/privatefs"
)

func ownershipIDs(info fs.FileInfo) (int, int, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("ownership metadata unavailable for %s", info.Name())
	}
	return int(stat.Uid), int(stat.Gid), nil
}

func lockLayoutState(opts *Options) (func(), error) {
	root, err := privatefs.Open(opts.path(executorStateDir))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := privatefs.OpenFile(root, "layout-trial.lock", os.O_CREATE|os.O_RDWR, 0600)
	if errors.Is(err, os.ErrExist) {
		file, err = privatefs.OpenFile(root, "layout-trial.lock", os.O_RDWR, 0600)
	}
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("layout lifecycle is already owned: %w", err)
	}
	closed := false
	return func() {
		if !closed {
			closed = true
			_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
			_ = file.Close()
		}
	}, nil
}

func ownershipIdentity(info fs.FileInfo) (uint64, uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("inode metadata unavailable for %s", info.Name())
	}
	return uint64(stat.Dev), uint64(stat.Ino), nil
}

// Bind chmod/chown to the admitted inode; neither a swapped symlink nor a
// shared-link regular file can redirect privileged metadata changes.
func restoreOwnershipAt(root *os.Root, path string, info fs.FileInfo, uid, gid int, mode os.FileMode, chown func(*os.Root, string, int, int) error) error {
	if info.Mode()&os.ModeSymlink != 0 {
		if chown != nil {
			return chown(root, path, uid, gid)
		}
		return root.Lchown(path, uid, gid)
	}
	file, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("custody inode %s changed during admission", path)
	}
	if opened.Mode().IsRegular() && linkCount(opened) > 1 {
		return fmt.Errorf("custody inode %s is shared", path)
	}
	if chown != nil {
		err = chown(root, path, uid, gid)
	} else {
		err = file.Chown(uid, gid)
	}
	if err != nil {
		return err
	}
	return file.Chmod(mode)
}
