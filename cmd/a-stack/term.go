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

// Linux only: raw mode and the window size use termios ioctls from syscall.

func termios(fd uintptr) (syscall.Termios, error) {
	var t syscall.Termios
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&t))); e != 0 {
		return t, e
	}
	return t, nil
}

func setTermios(fd uintptr, t *syscall.Termios) {
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(t)))
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
// again, so the process ends as it would without the picker.
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
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	resized := make(chan os.Signal, 1)
	signal.Notify(resized, syscall.SIGWINCH)
	defer signal.Stop(resized)
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
	keys := make(chan []byte)
	go func() {
		defer close(keys)
		buf := make([]byte, 64)
		for {
			n, err := in.Read(buf)
			if err != nil {
				return
			}
			keys <- append([]byte(nil), buf[:n]...)
		}
	}()
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
			switch p.key(k) {
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
			_ = syscall.Kill(os.Getpid(), s.(syscall.Signal))
			return sessionRow{}, errPickerCancelled
		case <-ctx.Done():
			return sessionRow{}, ctx.Err()
		}
	}
}
