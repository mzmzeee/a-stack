//go:build linux || darwin

package main

import (
	"os"
	"syscall"
	"testing"
)

// holdStateLock holds the state lock file as a serve does, until release.
func holdStateLock(t *testing.T, path string) (release func()) {
	t.Helper()

	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	return func() { lock.Close() }
}
