//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// The terminal of the Session picker on Linux and macOS: raw mode and the
// window size use termios ioctls from syscall. The ioctl numbers and select
// differ; see term_linux.go and term_darwin.go. Another system adds its own
// term_GOOS.go with isTerminal, terminalSize and runPicker.

func termios(fd uintptr) (syscall.Termios, error) {
	var t syscall.Termios
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlGetTermios, uintptr(unsafe.Pointer(&t))); e != 0 {
		return t, e
	}
	return t, nil
}

func setTermios(fd uintptr, t *syscall.Termios) {
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, fd, ioctlSetTermios, uintptr(unsafe.Pointer(t)))
}

func isTerminal(f *os.File) bool {
	_, err := termios(f.Fd())
	return err == nil
}

func terminalSize(f *os.File) (int, int) {
	var size struct{ rows, cols, x, y uint16 }
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&size))); e != 0 || size.cols == 0 {
		return 80, 24
	}
	return int(size.cols), int(size.rows)
}

var errPickerCancelled = errors.New("picker cancelled")

// runPicker draws p on out and reads keys from in until the Operator chooses a
// Session or cancels. It restores the terminal on every return and on panics.
// On SIGINT, SIGTERM or SIGHUP it restores the terminal and raises the signal
// again, so the process ends as it would without the picker. On SIGQUIT it
// restores the terminal and exits with 131.
func runPicker(ctx context.Context, in, out *os.File, p *picker, home string) (sessionRow, error) {
	saved, err := termios(in.Fd())
	if err != nil {
		return sessionRow{}, err
	}
	raw := saved
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Cc[syscall.VMIN], raw.Cc[syscall.VTIME] = 1, 0
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(signals)
	resized := make(chan os.Signal, 1)
	signal.Notify(resized, syscall.SIGWINCH)
	defer signal.Stop(resized)
	keys, stopKeys, err := readKeys(int(in.Fd()))
	if err != nil {
		return sessionRow{}, err
	}
	defer stopKeys()
	setTermios(in.Fd(), &raw)
	drawn := 0
	clear := func() {
		if drawn > 1 {
			fmt.Fprintf(out, "\r\x1b[%dA", drawn-1)
		}
		_, _ = io.WriteString(out, "\r\x1b[J")
	}
	defer func() {
		clear()
		_, _ = io.WriteString(out, "\x1b[?25h")
		setTermios(in.Fd(), &saved)
	}()
	_, _ = io.WriteString(out, "\x1b[?25l")
	for {
		width, height := terminalSize(out)
		clear()
		lines := p.render(width, height, home, time.Now())
		_, _ = io.WriteString(out, strings.Join(lines, "\n"))
		drawn = len(lines)
		select {
		case k, ok := <-keys:
			if !ok {
				return sessionRow{}, errPickerCancelled
			}
			switch p.input(k) {
			case pickChoose:
				return p.chosen(), nil
			case pickCancel:
				return sessionRow{}, errPickerCancelled
			}
		case <-resized:
			// The next loop reads the new size.
		case s := <-signals:
			clear()
			drawn = 0
			_, _ = io.WriteString(out, "\x1b[?25h")
			setTermios(in.Fd(), &saved)
			signal.Stop(signals)
			if s == syscall.SIGQUIT {
				// Go's own SIGQUIT handler prints a stack dump and exits
				// with 2; exit as a shell reports a quit instead.
				os.Exit(128 + int(syscall.SIGQUIT))
			}
			_ = syscall.Kill(os.Getpid(), s.(syscall.Signal))
			return sessionRow{}, errPickerCancelled
		case <-ctx.Done():
			return sessionRow{}, ctx.Err()
		}
	}
}

// readKeys reads chunks of input from fd until stop returns. It waits in
// select on fd and on a pipe, so stop wakes it, and nothing reads fd after
// stop.
func readKeys(fd int) (<-chan []byte, func(), error) {
	// macOS has no pipe2, so set close-on-exec under ForkLock, as os.Pipe does.
	var pipe [2]int
	syscall.ForkLock.RLock()
	err := syscall.Pipe(pipe[:])
	if err == nil {
		syscall.CloseOnExec(pipe[0])
		syscall.CloseOnExec(pipe[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, nil, err
	}
	if fd >= 1024 || pipe[0] >= 1024 {
		_ = syscall.Close(pipe[0])
		_ = syscall.Close(pipe[1])
		return nil, nil, errors.New("file descriptor too large for select")
	}
	keys := make(chan []byte)
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		defer close(keys)
		buf := make([]byte, 256)
		for {
			var set syscall.FdSet
			addFD(&set, fd)
			addFD(&set, pipe[0])
			if err := waitReadable(max(fd, pipe[0])+1, &set); err == syscall.EINTR {
				continue
			} else if err != nil || hasFD(&set, pipe[0]) {
				return
			}
			n, err := syscall.Read(fd, buf)
			if err == syscall.EINTR || err == syscall.EAGAIN {
				continue
			}
			if err != nil || n <= 0 {
				return
			}
			select {
			case keys <- append([]byte(nil), buf[:n]...):
			case <-quit:
				return
			}
		}
	}()
	stop := func() {
		close(quit)
		_, _ = syscall.Write(pipe[1], []byte{0})
		<-done
		_ = syscall.Close(pipe[0])
		_ = syscall.Close(pipe[1])
	}
	return keys, stop, nil
}

// fdBits is the number of descriptors in one word of syscall.FdSet.
const fdBits = int(unsafe.Sizeof(syscall.FdSet{}.Bits[0])) * 8

func addFD(set *syscall.FdSet, fd int)      { set.Bits[fd/fdBits] |= 1 << (fd % fdBits) }
func hasFD(set *syscall.FdSet, fd int) bool { return set.Bits[fd/fdBits]&(1<<(fd%fdBits)) != 0 }
