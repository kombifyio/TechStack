//go:build linux

// Package procfence proves that a previous executor process group is gone
// before kombify Guard admits conflicting work after a restart. Locking the
// journal is not enough: a detached executor can keep mutating the host.
package procfence

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// StartTicks returns the process start time in clock ticks since boot, which
// together with the PID identifies one process; zero means unknown.
func StartTicks(pid int) uint64 {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat") // #nosec G304 -- fixed procfs path with a numeric PID.
	if err != nil {
		return 0
	}
	stat := string(raw)
	// comm is parenthesized and may contain spaces; fields resume after the last ')'.
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return 0
	}
	fields := strings.Fields(stat[end+1:])
	// Field 22 of /proc/<pid>/stat is starttime; fields here start at field 3.
	if len(fields) < 20 {
		return 0
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0
	}
	return ticks
}

// GroupAlive reports whether any process of the group still exists.
func GroupAlive(pgid int) bool {
	if pgid <= 1 {
		return false
	}
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// EnsureGone makes sure the executor process group recorded before a restart no
// longer exists. A group that is gone needs nothing. A live group whose leader
// is provably the recorded process is killed and waited for. A live group whose
// leader is a different process is not ours and is left alone. An error means
// exclusion was not proven and conflicting work must wait.
func EnsureGone(pid, pgid int, startTicks uint64, timeout time.Duration) error {
	if pgid <= 1 || !GroupAlive(pgid) {
		return nil
	}
	if startTicks != 0 {
		if leader := StartTicks(pgid); leader != 0 && leader != startTicks {
			return nil
		}
	}
	// EPERM: the group belongs to the privileged executor, which stops its whole
	// process tree when the daemon disconnects. Wait for it instead of failing.
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) && !errors.Is(err, syscall.EPERM) {
		return fmt.Errorf("stop previous executor group %d: %w", pgid, err)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !GroupAlive(pgid) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("previous executor group %d is still alive after %s", pgid, timeout)
}
