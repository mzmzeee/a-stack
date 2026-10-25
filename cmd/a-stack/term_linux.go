package main

import "syscall"

// The termios ioctls and select of Linux, for term_unix.go.
const (
	ioctlGetTermios = syscall.TCGETS
	ioctlSetTermios = syscall.TCSETS
)

// waitReadable blocks in select until a descriptor in set is readable.
func waitReadable(n int, set *syscall.FdSet) error {
	_, err := syscall.Select(n, set, nil, nil, nil)
	return err
}
