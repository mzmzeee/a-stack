package stack

import (
	"errors"
	"strings"
	"syscall"
	"unsafe"
)

var procFinalPath = kernel32.NewProc("GetFinalPathNameByHandleW")

// ResolvePath follows file symlinks and directory junctions. filepath.EvalSymlinks
// can reject a junction in the middle of a path as not a directory.
func ResolvePath(path string) (string, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	h, err := syscall.CreateFile(p, 0, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer syscall.CloseHandle(h)
	buf := make([]uint16, 260)
	for {
		n, _, e := procFinalPath.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
		if n == 0 {
			// The handle exists. Do not expose a path-not-found errno here:
			// statePath must not mistake a resolution failure for a missing ancestor.
			return "", errors.New("cannot resolve opened path: " + e.Error())
		}
		if n >= uintptr(len(buf)) {
			buf = make([]uint16, n+1)
			continue
		}
		resolved := syscall.UTF16ToString(buf[:n])
		if strings.HasPrefix(resolved, `\\?\UNC\`) {
			return `\\` + strings.TrimPrefix(resolved, `\\?\UNC\`), nil
		}
		return strings.TrimPrefix(resolved, `\\?\`), nil
	}
}

// UNC and device namespaces cannot be used for SQLite state.
func localStatePath(path string) bool {
	path = strings.ReplaceAll(path, "/", `\`)
	return len(path) >= 3 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) && path[1:3] == `:\`
}
