//go:build windows

package agent

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func checkStackKitRuntimeAncestor(root *os.Root, protectedSystemDriveChild bool) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	// The Windows system drive is normally owned by its servicing authority.
	servicing, err := windows.StringToSid("S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464")
	if err != nil {
		return err
	}
	trusted := func(sid *windows.SID) bool {
		return sid != nil && (sid.Equals(user.User.Sid) || sid.Equals(servicing) || sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid))
	}
	security, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := security.Owner()
	if err != nil || !trusted(owner) {
		return fmt.Errorf("public runtime ancestor has an untrusted owner")
	}
	acl, _, err := security.DACL()
	if err != nil || acl == nil {
		return fmt.Errorf("public runtime ancestor requires a restricted DACL")
	}
	const deleteChild = 0x00000040 // FILE_DELETE_CHILD, not exported by x/sys/windows.
	const writeAccess = windows.GENERIC_ALL | windows.GENERIC_WRITE | windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA |
		windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES | windows.DELETE | deleteChild | windows.WRITE_DAC | windows.WRITE_OWNER
	forbidden := windows.ACCESS_MASK(writeAccess)
	if protectedSystemDriveChild {
		// Creating unrelated siblings cannot replace the existing child whose
		// owner and effective ACL the path admission already checked.
		forbidden &^= windows.FILE_APPEND_DATA // FILE_ADD_SUBDIRECTORY.
	}
	for index := uint32(0); index < uint32(acl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, index, &ace); err != nil {
			return err
		}
		// An inheritance-only entry does not grant access to this inode;
		// the actual child's effective ACL is admitted independently.
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("public runtime ancestor has an unsupported access grant")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if ace.Mask&forbidden != 0 && !trusted(sid) {
			return fmt.Errorf("public runtime ancestor grants write access to another principal")
		}
	}
	return nil
}

func trustedStackKitTempAncestor(string, os.FileInfo) bool { return false }

func tryStackKitRuntimeLock(file *os.File, exclusive bool) (bool, error) {
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	err := windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, new(windows.Overlapped))
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}

func releaseStackKitRuntimeLock(file *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, new(windows.Overlapped))
	_ = file.Close()
}
