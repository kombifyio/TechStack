//go:build windows

package journal

import (
	"errors"
	"os"

	"github.com/kombifyio/techstack/internal/privatefs"
	"golang.org/x/sys/windows"
)

func acquireLock(root *os.Root, path string) (*os.File, error) {
	file, err := privatefs.OpenFile(root, path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	overlapped := new(windows.Overlapped)
	err = windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
	if err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return file, nil
}

func releaseLock(file *os.File) error {
	overlapped := new(windows.Overlapped)
	unlockErr := windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
	return errors.Join(unlockErr, file.Close())
}

// Directory handles cannot be fsynced on Windows; renames are durable through
// the file system journal.
func syncDir(*os.Root) error { return nil }

func freeBytes(root *os.Root) (uint64, error) {
	file, err := root.Open(".")
	if err != nil {
		return 0, err
	}
	defer file.Close()
	buffer := make([]uint16, 32768)
	length, err := windows.GetFinalPathNameByHandle(windows.Handle(file.Fd()), &buffer[0], uint32(len(buffer)), 0 /* FILE_NAME_NORMALIZED | VOLUME_NAME_DOS */)
	if err != nil {
		return 0, err
	}
	if length >= uint32(len(buffer)) {
		return 0, windows.ERROR_INSUFFICIENT_BUFFER
	}
	pointer, err := windows.UTF16PtrFromString(windows.UTF16ToString(buffer[:length]))
	if err != nil {
		return 0, err
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(pointer, &available, &total, &free); err != nil {
		return 0, err
	}
	return available, nil
}

func isNoSpace(err error) bool {
	return errors.Is(err, windows.ERROR_DISK_FULL) || errors.Is(err, windows.ERROR_HANDLE_DISK_FULL)
}

// Private custody admission uses the actual NTFS owner/DACL, not POSIX modes.
