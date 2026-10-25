package main

import (
	"errors"
	"syscall"
	"unsafe"
)

var procShellExecuteW = syscall.NewLazyDLL("shell32.dll").NewProc("ShellExecuteW")

// openURL opens url with ShellExecuteW, as a double click on a link does. No
// command line parses the URL on the way.
func openURL(url string) error {
	verb, err := syscall.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := syscall.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	const showNormal = 1
	// A result above 32 means success.
	if r, _, _ := procShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)), 0, 0, showNormal); r <= 32 {
		return errors.New("ShellExecuteW failed")
	}
	return nil
}
