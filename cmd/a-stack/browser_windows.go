package main

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	procShellExecuteW  = syscall.NewLazyDLL("shell32.dll").NewProc("ShellExecuteW")
	ole32              = syscall.NewLazyDLL("ole32.dll")
	procCoInitializeEx = ole32.NewProc("CoInitializeEx")
	procCoUninitialize = ole32.NewProc("CoUninitialize")
)

// Shell extensions may require a single-threaded COM apartment. Keep the
// initialization, action and balanced cleanup on the same OS thread.
func withBrowserCOM(action func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	const apartmentThreaded, disableOLE1DDE = 0x2, 0x4
	hr, _, _ := procCoInitializeEx.Call(0, apartmentThreaded|disableOLE1DDE)
	if int32(hr) < 0 {
		return fmt.Errorf("CoInitializeEx failed (HRESULT 0x%08x)", uint32(hr))
	}
	defer procCoUninitialize.Call() // S_FALSE also needs CoUninitialize.
	return action()
}

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
	return withBrowserCOM(func() error {
		const showNormal = 1
		// A result above 32 means success.
		if r, _, _ := procShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)), 0, 0, showNormal); r <= 32 {
			return errors.New("ShellExecuteW failed")
		}
		return nil
	})
}
