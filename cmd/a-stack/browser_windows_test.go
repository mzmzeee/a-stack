package main

import (
	"errors"
	"runtime"
	"testing"
	"unsafe"
)

func TestBrowserCOMApartmentAndCleanup(t *testing.T) {
	// Keep the checks before and after the helper on one otherwise unused thread.
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		getApartment := ole32.NewProc("CoGetApartmentType")
		var apartment, qualifier uint32
		get := func() uintptr {
			hr, _, _ := getApartment.Call(uintptr(unsafe.Pointer(&apartment)), uintptr(unsafe.Pointer(&qualifier)))
			return hr
		}
		sentinel := errors.New("action failed")
		err := withBrowserCOM(func() error {
			if int32(get()) < 0 || apartment != 0 && apartment != 3 { // STA or MAINSTA
				return errors.New("browser action has no single-threaded apartment")
			}
			// An already initialized STA returns S_FALSE, which must also be
			// balanced without uninitializing the outer call's apartment.
			if err := withBrowserCOM(func() error { return nil }); err != nil {
				return err
			}
			if int32(get()) < 0 {
				return errors.New("nested call uninitialized the outer apartment")
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			if err == nil {
				err = errors.New("browser helper lost the action's error")
			}
			done <- err
			return
		}
		if int32(get()) >= 0 {
			done <- errors.New("browser helper leaked its COM initialization")
			return
		}
		done <- nil
	}()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
