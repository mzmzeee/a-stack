package main

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"github.com/mzmzeee/a-stack/internal/stack"
)

var (
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
	procFreeConsole           = kernel32.NewProc("FreeConsole")
)

// detachConsole closes the console when Windows made it just for this
// process, as for the Task Scheduler task, so that no window stays open.
// The output then goes to serviceLog(state), which starts again past 1 MiB;
// detachConsole returns it, or nil when serve keeps its console.
func detachConsole(state string) *os.File {
	var pids [2]uint32
	if n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids))); n != 1 {
		return nil
	}
	f, err := stack.OpenServiceLog(state)
	if err != nil {
		return nil
	}
	_, _, _ = procFreeConsole.Call()
	os.Stderr = f
	return f
}

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
