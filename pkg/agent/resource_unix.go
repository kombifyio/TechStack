//go:build !windows

package agent

import "syscall"

// getDiskUsage returns disk used and total bytes for the root filesystem.
// On Unix-like systems (Linux and macOS), this queries the "/" mount point via
// statfs, which is portable across the unix family. Per-OS RAM detection lives
// in resource_linux.go / resource_darwin.go because the RAM syscalls differ.
// Returns (0, 0) on error to allow graceful degradation.
func getDiskUsage() (used uint64, total uint64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return 0, 0
	}

	// statfs.Bsize is platform-typed (int32/int64 depending on kernel).
	// Reject negative values defensively so the uint64 cast below cannot
	// silently wrap to a huge number (gosec G115 integer overflow gate).
	if stat.Bsize <= 0 {
		return 0, 0
	}
	bsize := uint64(stat.Bsize) //#nosec G115 -- guarded by positive check above

	// Total = blocks * block size
	// Available = available blocks * block size (for non-root users)
	// Used = Total - Free (free blocks, not available)
	total = stat.Blocks * bsize
	free := stat.Bfree * bsize
	used = total - free

	return used, total
}
