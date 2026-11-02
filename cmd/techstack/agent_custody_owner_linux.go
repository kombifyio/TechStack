//go:build linux

package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"

	"github.com/kombifyio/techstack/pkg/guardlayout"
)

// splitCustodyOwner names the daemon user when root runs a command on a
// privilege-split Guard: its state directory then belongs to that user, and
// node custody admits only its owner. Only the layout's own user counts; a
// directory anyone else owns is left to the custody check to refuse.
func splitCustodyOwner(stateDir string) (*syscall.Credential, string, bool) {
	if os.Geteuid() != 0 {
		return nil, "", false
	}
	info, err := os.Lstat(stateDir)
	if err != nil || !info.IsDir() {
		return nil, "", false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid == 0 {
		return nil, "", false
	}
	owner, err := user.LookupId(strconv.FormatUint(uint64(stat.Uid), 10))
	if err != nil || owner.Username != guardlayout.DefaultUser {
		return nil, "", false
	}
	gid, err := strconv.ParseUint(owner.Gid, 10, 32)
	if err != nil {
		return nil, "", false
	}
	return &syscall.Credential{Uid: stat.Uid, Gid: uint32(gid), Groups: []uint32{}}, owner.Username, true
}

// custodyChildArgs repeats `agent <command>` with the flags the operator set,
// except the ones root replaces for the daemon-user child.
func custodyChildArgs(command string, flags *flag.FlagSet, replaced ...string) []string {
	args := []string{"agent", command}
	flags.Visit(func(f *flag.Flag) {
		if !slices.Contains(replaced, f.Name) {
			args = append(args, "--"+f.Name+"="+f.Value.String())
		}
	})
	return args
}

// absolutePaths makes the operator's paths absolute as root resolves them: the
// child runs in /. An empty path stays empty.
func absolutePaths(paths ...*string) error {
	for _, path := range paths {
		if *path == "" {
			continue
		}
		absolute, err := filepath.Abs(*path)
		if err != nil {
			return err
		}
		*path = absolute
	}
	return nil
}

// runAsCustodyOwner runs this executable again as the daemon user of a
// privilege-split Guard. The one-time secret goes on its stdin, never into its
// arguments, and the files root opened for it are its descriptors from 3 on.
func runAsCustodyOwner(ctx context.Context, credential *syscall.Credential, args []string, secret []byte, files []*os.File, stdout io.Writer) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate the Guard executable: %w", err)
	}
	child := exec.CommandContext(ctx, executable, args...) // #nosec G204 -- this executable with its own parsed flags.
	child.Stdin, child.Stdout, child.Stderr, child.Dir = bytes.NewReader(secret), stdout, os.Stderr, "/"
	child.ExtraFiles = files
	child.SysProcAttr = &syscall.SysProcAttr{Credential: credential}
	return child.Run()
}
