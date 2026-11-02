//go:build linux

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/kombifyio/techstack/internal/privatefs"
	"golang.org/x/sys/unix"
)

// handoffAsCustodyOwner ends a prior tenant's custody on a privilege-split
// Guard when root runs the handoff. Root creates the archive where only root
// reaches it, so the next binding's daemon never can, and hands that one file
// to a child running as the daemon user. The child verifies the confirmation,
// archives into the inherited file and removes the payloads, the identity
// state and the spool, so the journal, key and receipt stay the daemon's. It
// reports false when the state directory is not a split Guard's.
func handoffAsCustodyOwner(ctx context.Context, flags *flag.FlagSet, stateDir, enrollmentFile string, target handoffTarget, confirmation []byte, stdout io.Writer) (bool, error) {
	credential, name, ok := splitCustodyOwner(stateDir)
	if !ok {
		return false, nil
	}
	if err := absolutePaths(&stateDir, &enrollmentFile); err != nil {
		return true, err
	}
	dir, archive, err := createRootArchive(target)
	if err != nil {
		return true, fmt.Errorf("create the handoff archive %s: %w", target.path(), err)
	}
	defer dir.Close()
	defer archive.Close()
	archiveFlag := "--export-file=" + target.path()
	if target.quarantine {
		archiveFlag = "--quarantine-dir=" + target.dir
	}
	args := append(custodyChildArgs("handoff", flags, "confirmation-file", "state-dir", "enrollment-file", "export-file", "quarantine-dir", "archive-fd"),
		"--state-dir="+stateDir, "--enrollment-file="+enrollmentFile, archiveFlag, "--confirmation-file=-", "--archive-fd=3")
	if err := runAsCustodyOwner(ctx, credential, args, confirmation, []*os.File{archive}, stdout); err != nil {
		// A child that stopped before its archive was complete leaves the empty
		// file, which would refuse a repeated handoff. A written archive stays,
		// as it does when removing the payloads after it fails.
		if held, statErr := archive.Stat(); statErr == nil && held.Size() == 0 {
			if current, lstatErr := dir.Lstat(target.name); lstatErr == nil && os.SameFile(held, current) {
				_ = dir.Remove(target.name)
			}
		}
		return true, fmt.Errorf("the handoff as %s did not complete: %w", name, err)
	}
	return true, nil
}

// inheritedHandoffArchive admits the archive root created for this daemon-user
// child of a privilege-split Guard: a write-only descriptor of a new, empty,
// owner-only, single-link regular file. Root itself never takes this path.
func inheritedHandoffArchive(fd int, name string) (*os.File, error) {
	if fd < 3 || os.Geteuid() == 0 {
		return nil, errors.New("--archive-fd is only for the daemon-user child of a privilege-split Guard")
	}
	mode, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return nil, fmt.Errorf("no inherited archive descriptor: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	if info, err := file.Stat(); err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if ok && mode&unix.O_ACCMODE == unix.O_WRONLY && info.Mode().IsRegular() &&
			stat.Nlink == 1 && info.Mode().Perm()&0o077 == 0 && info.Size() == 0 {
			return file, nil
		}
	}
	_ = file.Close()
	return nil, errors.New("the inherited archive is not a new write-only, owner-only regular file")
}

// createRootArchive creates the archive as root's new owner-only, single-link
// regular file, below directories only root controls: an existing file, link
// or symlink is never followed or replaced, and no other user can redirect the
// path or swap the file. A quarantine directory root creates on the way is
// its own as well.
func createRootArchive(target handoffTarget) (*os.Root, *os.File, error) {
	parent := target.dir
	if target.quarantine {
		parent = filepath.Dir(target.dir)
	}
	resolved, err := rootOnlyDirectory(parent)
	if err != nil {
		return nil, nil, err
	}
	dir, err := os.OpenRoot(resolved)
	if err != nil {
		return nil, nil, err
	}
	if target.quarantine {
		anchor := dir
		dir, err = privatefs.OpenChild(anchor, filepath.Base(target.dir))
		_ = anchor.Close()
		if err != nil {
			return nil, nil, err
		}
	}
	file, err := privatefs.OpenFile(dir, target.name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = dir.Close()
		return nil, nil, err
	}
	return dir, file, nil
}

// rootOnlyDirectory resolves dir and refuses it unless it and every directory
// above it belong to root and no other user can write to them: the daemon user,
// or anyone else, could otherwise move or replace what root creates there.
func rootOnlyDirectory(dir string) (string, error) {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	for current := resolved; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !info.IsDir() || !ok || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
			return "", fmt.Errorf("%s must be a directory only root owns and can write to", current)
		}
		if current == filepath.Dir(current) {
			return resolved, nil
		}
	}
}
