package stack

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// The private files of the state directory on Windows. Mode bits mean
// little there, so a-stack makes the state and credential folders with an
// access list for SYSTEM, the Administrators and this user only, and files
// inside inherit it. Files open without following a link and share reading,
// writing and deleting, so that a writer can rename over a file that a
// reader has open. The state lock is the lock file opened with no sharing:
// a second serve gets a sharing violation, and Windows lets go of it when
// the process ends.

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	advapi32                = syscall.NewLazyDLL("advapi32.dll")
	procGetDiskFreeSpaceExW = kernel32.NewProc("GetDiskFreeSpaceExW")
	procConvertStringSD     = advapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
)

// errorSharingViolation is ERROR_SHARING_VIOLATION, which syscall does not name.
const errorSharingViolation syscall.Errno = 32

// checkPrivate checks type; checkPrivateHandle checks the ACL on the open handle.
func checkPrivate(info os.FileInfo, directory bool) error {
	attributes, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok || attributes.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return failure("invalid_input", "state and credential paths must be plain files and folders, not links")
	}
	return nil
}

func createFile(path string, write bool, share uint32) (*os.File, error) {
	access, disposition := uint32(syscall.GENERIC_READ), uint32(syscall.OPEN_EXISTING)
	if write {
		access, disposition = syscall.GENERIC_READ|syscall.GENERIC_WRITE, syscall.OPEN_ALWAYS
	}
	return openWindowsFile(path, access, disposition, share)
}

func openPrivateAppend(path string) (*os.File, error) {
	return openWindowsFile(path, syscall.GENERIC_READ|syscall.FILE_APPEND_DATA, syscall.OPEN_EXISTING, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE)
}

func openWindowsFile(path string, access, disposition, share uint32) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p, access, share, nil, disposition, syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), path)
	info, err := f.Stat()
	if err == nil {
		err = checkPrivate(info, false)
	}
	if err == nil {
		err = checkPrivateHandle(f)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// openPrivate opens path without following a link: for reading, or with
// write for reading and writing, created when missing.
func openPrivate(path string, write bool) (*os.File, error) {
	return createFile(path, write, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE)
}

// lockFile opens the lock file of a state directory with no sharing, which
// holds it until unlockFile.
func lockFile(path string) (*os.File, error) {
	f, err := createFile(path, true, 0)
	if errors.Is(err, errorSharingViolation) {
		return nil, ErrStateOwned
	}
	if err != nil {
		return nil, storageError(err)
	}
	info, err := f.Stat()
	if err == nil {
		err = checkPrivate(info, false)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func unlockFile(f *os.File) { _ = f.Close() }

// privateSDDL is the access list of a private folder: full control for
// SYSTEM, the Administrators and the user sid, inherited by every file and
// folder inside, and nothing inherited from above (P).
func privateSDDL(sid string) string {
	return "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + sid + ")"
}

// MkdirPrivate creates dir and its missing parents. dir itself gets the
// private access list. An existing folder keeps its ACL and must pass the
// same privacy check before it is used.
func MkdirPrivate(dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return err
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
	sid, err := user.User.Sid.String()
	if err != nil {
		return err
	}
	sddl, err := syscall.UTF16PtrFromString(privateSDDL(sid))
	if err != nil {
		return err
	}
	var descriptor uintptr
	if r, _, e := procConvertStringSD.Call(uintptr(unsafe.Pointer(sddl)), 1, uintptr(unsafe.Pointer(&descriptor)), 0); r == 0 {
		return e
	}
	defer syscall.LocalFree(syscall.Handle(descriptor))
	p, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	sa := syscall.SecurityAttributes{SecurityDescriptor: descriptor}
	sa.Length = uint32(unsafe.Sizeof(sa))
	if err = syscall.CreateDirectory(p, &sa); errors.Is(err, syscall.ERROR_ALREADY_EXISTS) {
		return checkPrivateDirectory(dir)
	}
	return err
}

// defaultStateRoot is the folder for the state directory when
// XDG_STATE_HOME is not set: %LOCALAPPDATA%.
func defaultStateRoot() (string, error) {
	if root := os.Getenv("LOCALAPPDATA"); filepath.IsAbs(root) {
		return root, nil
	}
	return "", failure("invalid_input", "LOCALAPPDATA is not set; give --state-dir")
}

// renameBusy reports whether a rename failed because another process has
// the target open: access is denied, or the sharing is violated.
func renameBusy(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) || errors.Is(err, errorSharingViolation)
}

// syncDir does nothing: Windows cannot flush a folder, and NTFS journals
// its renames.
func syncDir(string) error { return nil }

// diskFree is the free space that this user can use on the volume of path.
// GetDiskFreeSpaceExW takes a folder, so it asks for the folder of path.
func diskFree(path string) (uint64, error) {
	p, err := syscall.UTF16PtrFromString(filepath.Dir(path))
	if err != nil {
		return 0, err
	}
	var free uint64
	if r, _, e := procGetDiskFreeSpaceExW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&free)), 0, 0); r == 0 {
		return 0, e
	}
	return free, nil
}
