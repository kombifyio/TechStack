//go:build windows

package agent

import (
	"syscall"
	"unsafe"
)

// getDiskUsage returns disk used and total bytes for the C: drive on Windows.
// Uses GetDiskFreeSpaceExW to query disk space information.
// Returns (0, 0) on error to allow graceful degradation.
func getDiskUsage() (used uint64, total uint64) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpaceEx := kernel32.NewProc("GetDiskFreeSpaceExW")

	var freeBytesAvailable, totalBytes, totalFreeBytes uint64

	// Query C: drive (most common Windows root)
	path, err := syscall.UTF16PtrFromString("C:\\")
	if err != nil {
		return 0, 0
	}

	ret, _, _ := getDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(path)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFreeBytes)),
	)

	if ret == 0 {
		return 0, 0
	}

	return totalBytes - totalFreeBytes, totalBytes
}

// memoryStatusEx matches Windows MEMORYSTATUSEX structure.
type memoryStatusEx struct {
	dwLength                uint32
	dwMemoryLoad            uint32
	ullTotalPhys            uint64
	ullAvailPhys            uint64
	ullTotalPageFile        uint64
	ullAvailPageFile        uint64
	ullTotalVirtual         uint64
	ullAvailVirtual         uint64
	ullAvailExtendedVirtual uint64
}

// getTotalSystemRAM returns the total physical RAM in bytes on Windows.
// Uses GlobalMemoryStatusEx to query memory information.
// Returns 0 on error to allow graceful degradation.
func getTotalSystemRAM() uint64 {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	globalMemoryStatusEx := kernel32.NewProc("GlobalMemoryStatusEx")

	var memStatus memoryStatusEx
	memStatus.dwLength = uint32(unsafe.Sizeof(memStatus))

	ret, _, _ := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&memStatus)))
	if ret == 0 {
		return 0
	}

	return memStatus.ullTotalPhys
}
