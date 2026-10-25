package main

import (
	"path/filepath"
	"syscall"
)

// tryLock holds the file .lock in the folder dir open with no sharing,
// without waiting. release lets it go; ok is false when another process
// holds it.
func tryLock(dir string) (release func(), ok bool) {
	p, err := syscall.UTF16PtrFromString(filepath.Join(dir, ".lock"))
	if err != nil {
		return nil, false
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, false
	}
	return func() { syscall.CloseHandle(h) }, true
}
