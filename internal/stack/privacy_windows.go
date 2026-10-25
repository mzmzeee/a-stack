package stack

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	procGetSecurityInfo = advapi32.NewProc("GetSecurityInfo")
	procGetAce          = advapi32.NewProc("GetAce")
)

// Read security from the same handle used for I/O, including the exclusive
// service lock. Reopening the name would check a different object after a rename.
func checkPrivateHandle(f *os.File) error {
	const fileObject, ownerAndDACL = 1, 5
	var owner *syscall.SID
	var acl *struct {
		Revision, Reserved     byte
		Size, Count, Reserved2 uint16
	}
	var descriptor uintptr
	if r, _, _ := procGetSecurityInfo.Call(f.Fd(), fileObject, ownerAndDACL, uintptr(unsafe.Pointer(&owner)), 0, uintptr(unsafe.Pointer(&acl)), 0, uintptr(unsafe.Pointer(&descriptor))); r != 0 {
		return syscall.Errno(r)
	}
	defer syscall.LocalFree(syscall.Handle(descriptor))
	denied := failure("invalid_input", "state and credential paths must allow access only to this user, SYSTEM and Administrators")
	if owner == nil || acl == nil {
		return denied
	}
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	current, err := user.User.Sid.String()
	if err != nil {
		return err
	}
	allowed := func(sid *syscall.SID) bool {
		s, err := sid.String()
		return err == nil && (s == current || s == "S-1-5-18" || s == "S-1-5-32-544")
	}
	if !allowed(owner) {
		return denied
	}
	for i := uint16(0); i < acl.Count; i++ {
		var ace *struct {
			Type, Flags byte
			Size        uint16
			Mask        uint32
			SID         uint32
		}
		if r, _, e := procGetAce.Call(uintptr(unsafe.Pointer(acl)), uintptr(i), uintptr(unsafe.Pointer(&ace))); r == 0 {
			return e
		}
		// A deny ACE cannot grant access. Reject unfamiliar ACE types rather
		// than trying to interpret object-specific or callback grants.
		if ace.Type == 1 {
			continue
		}
		if ace.Type != 0 || ace.Size < 12 || !allowed((*syscall.SID)(unsafe.Pointer(&ace.SID))) {
			return denied
		}
	}
	return nil
}

func checkPrivateDirectory(path string) error {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	const readControl = 0x20000
	h, err := syscall.CreateFile(p, readControl, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err = checkPrivate(info, true); err != nil {
		return err
	}
	return checkPrivateHandle(f)
}
