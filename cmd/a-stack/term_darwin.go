package main

import "syscall"

// The termios ioctls and select of macOS, for term_unix.go.
const (
	ioctlGetTermios = syscall.TIOCGETA
	ioctlSetTermios = syscall.TIOCSETA
)

// waitReadable blocks in select until a descriptor in set is readable.
func waitReadable(n int, set *syscall.FdSet) error {
	return syscall.Select(n, set, nil, nil, nil)
}
