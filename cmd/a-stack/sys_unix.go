//go:build linux || darwin

package main

import (
	"os"
	"syscall"
)

// detachConsole is for Windows: serve keeps its terminal here.
func detachConsole(string) *os.File { return nil }

// tryLock takes an exclusive flock on the folder dir without waiting.
// release lets it go; ok is false when another process holds it.
func tryLock(dir string) (release func(), ok bool) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, false
	}

	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return nil, false
	}

	return func() { f.Close() }, true
}
