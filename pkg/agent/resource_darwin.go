//go:build darwin

package agent

import "golang.org/x/sys/unix"

// getTotalSystemRAM returns the total physical RAM in bytes on macOS by reading
// the hw.memsize sysctl. macOS has no sysinfo(2), so it cannot share the Linux
// implementation.
// Returns 0 on error to allow graceful degradation.
func getTotalSystemRAM() uint64 {
	mem, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return mem
}
