package privatefs

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var reopenFile = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")

// Windows file modes do not express directory access. Inspect the ACL of the
// admitted handle instead; the user, system and administrators retain custody.
func validateOwner(directory *os.File, _ os.FileInfo) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	trusted := func(sid *windows.SID) bool {
		return sid != nil && (sid.Equals(user.User.Sid) || sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid))
	}
	security, err := windows.GetSecurityInfo(windows.Handle(directory.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := security.Owner()
	if err != nil || !trusted(owner) {
		return fmt.Errorf("%w: private custody has an untrusted owner", ErrUnsafeCustody)
	}
	acl, _, err := security.DACL()
	if err != nil || acl == nil {
		return fmt.Errorf("%w: private custody requires a restricted ACL", ErrUnsafeCustody)
	}
	for index := uint32(0); index < uint32(acl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, index, &ace); err != nil {
			return err
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("%w: private custody has an unsupported access grant", ErrUnsafeCustody)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if ace.Mask != 0 && !trusted(sid) {
			return fmt.Errorf("%w: private custody grants access to another principal", ErrUnsafeCustody)
		}
	}
	return nil
}

func initializeDirectory(file *os.File, info os.FileInfo) error { return setPrivateACL(file) }

func protectFile(file *os.File, mode os.FileMode) error {
	if mode.Perm()&0o077 == 0 {
		return setPrivateACL(file)
	}
	return file.Chmod(mode)
}

func setPrivateACL(file *os.File) error {
	// EXPLICIT_ACCESS stores SID addresses as uintptr. Keep their backing
	// allocations pinned until the native ACL builder and setter have finished.
	var pins runtime.Pinner
	defer pins.Unpin()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	pins.Pin(user.User.Sid)
	security, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := security.Owner()
	if err != nil || owner == nil || !(owner.Equals(user.User.Sid) || owner.IsWellKnown(windows.WinLocalSystemSid) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid)) {
		return fmt.Errorf("%w: private custody has an untrusted owner", ErrUnsafeCustody)
	}
	var entries []windows.EXPLICIT_ACCESS
	for _, sidType := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid} {
		sid, err := windows.CreateWellKnownSid(sidType)
		if err != nil {
			return err
		}
		pins.Pin(sid)
		entries = append(entries, windows.EXPLICIT_ACCESS{AccessPermissions: windows.GENERIC_ALL, AccessMode: windows.SET_ACCESS, Inheritance: windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT, Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_UNKNOWN, TrusteeValue: windows.TrusteeValueFromSID(sid)}})
	}
	entries = append(entries, windows.EXPLICIT_ACCESS{AccessPermissions: windows.GENERIC_ALL, AccessMode: windows.SET_ACCESS, Inheritance: windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT, Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER, TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid)}})
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	// Go file handles do not request WRITE_DAC. ReOpenFile obtains that
	// permission for the same inode without resolving a mutable pathname.
	// https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-reopenfile
	value, _, callErr := reopenFile.Call(file.Fd(), windows.WRITE_DAC|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_FLAG_BACKUP_SEMANTICS)
	handle := windows.Handle(value)
	if handle == windows.InvalidHandle {
		return callErr
	}
	defer windows.CloseHandle(handle)
	return windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func validateFile(file *os.File, info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: private custody leaf must be regular", ErrUnsafeCustody)
	}
	var metadata windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &metadata); err != nil {
		return err
	}
	if metadata.NumberOfLinks != 1 {
		return fmt.Errorf("%w: private custody leaf must have a single link", ErrUnsafeCustody)
	}
	return validateOwner(file, info)
}
