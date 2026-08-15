//go:build linux

package agent

import "syscall"

// getTotalSystemRAM returns the total physical RAM in bytes on Linux using the
// sysinfo(2) syscall. Sysinfo is Linux-only, which is why this lives in a
// linux-tagged file rather than the shared unix file.
// Returns 0 on error to allow graceful degradation.
func getTotalSystemRAM() uint64 {
	var info syscall.Sysinfo_t
	if err := syscall.Sysinfo(&info); err != nil {
		return 0
	}
	// Convert to bytes: total RAM * memory unit.
	return info.Totalram * uint64(info.Unit)
}
