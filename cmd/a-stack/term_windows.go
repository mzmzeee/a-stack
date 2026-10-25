package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

// The terminal of the Session picker on Windows: console modes stand in for
// termios. Virtual terminal input turns the arrow keys into the same escape
// sequences as on Linux, and virtual terminal processing makes an older
// console draw the picker's escape sequences. A window size event stands in
// for SIGWINCH.

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleMode             = kernel32.NewProc("SetConsoleMode")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
	procReadConsoleInputW          = kernel32.NewProc("ReadConsoleInputW")
)

const (
	enableProcessedInput            = 0x1
	enableLineInput                 = 0x2
	enableEchoInput                 = 0x4
	enableWindowInput               = 0x8
	enableVirtualTerminalInput      = 0x200
	enableProcessedOutput           = 0x1
	enableVirtualTerminalProcessing = 0x4
	keyEvent                        = 0x1
	windowBufferSizeEvent           = 0x4
)

// inputRecord is an INPUT_RECORD holding a KEY_EVENT_RECORD.
type inputRecord struct {
	eventType uint16
	_         uint16
	keyDown   int32
	repeat    uint16
	keyCode   uint16
	scanCode  uint16
	char      uint16
	state     uint32
}

func setConsoleMode(h syscall.Handle, mode uint32) error {
	if r, _, e := procSetConsoleMode.Call(uintptr(h), uintptr(mode)); r == 0 {
		return e
	}
	return nil
}

func isTerminal(f *os.File) bool {
	var mode uint32
	return syscall.GetConsoleMode(syscall.Handle(f.Fd()), &mode) == nil
}

func terminalSize(f *os.File) (int, int) {
	var info struct {
		size, cursor [2]int16
		attributes   uint16
		window       [4]int16 // left, top, right, bottom
		maxSize      [2]int16
	}
	if r, _, _ := procGetConsoleScreenBufferInfo.Call(f.Fd(), uintptr(unsafe.Pointer(&info))); r == 0 || info.window[2] <= info.window[0] {
		return 80, 24
	}
	return int(info.window[2]-info.window[0]) + 1, int(info.window[3]-info.window[1]) + 1
}

// runPicker draws p on out and reads keys from in until the Operator chooses
// a Session or cancels. Ctrl-C arrives as a key and cancels. It restores the
// console modes on every return and on panics.
func runPicker(ctx context.Context, in, out *os.File, p *picker, home string) (sessionRow, error) {
	inHandle, outHandle := syscall.Handle(in.Fd()), syscall.Handle(out.Fd())
	var inMode, outMode uint32
	if err := syscall.GetConsoleMode(inHandle, &inMode); err != nil {
		return sessionRow{}, err
	}
	if err := syscall.GetConsoleMode(outHandle, &outMode); err != nil {
		return sessionRow{}, err
	}
	raw := inMode&^(enableProcessedInput|enableLineInput|enableEchoInput) | enableVirtualTerminalInput | enableWindowInput
	if err := setConsoleMode(inHandle, raw); err != nil {
		return sessionRow{}, err
	}
	if err := setConsoleMode(outHandle, outMode|enableProcessedOutput|enableVirtualTerminalProcessing); err != nil {
		_ = setConsoleMode(inHandle, inMode)
		return sessionRow{}, err
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	keys, resized, stopKeys := readConsoleKeys(inHandle)
	defer stopKeys()
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
		_ = setConsoleMode(inHandle, inMode)
		_ = setConsoleMode(outHandle, outMode)
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
		case <-signals:
			return sessionRow{}, errPickerCancelled
		case <-ctx.Done():
			return sessionRow{}, ctx.Err()
		}
	}
}

// readConsoleKeys sends the characters typed into the console h until stop
// returns, and a value on resized when the window size changes. It waits
// on h with a timeout, so stop ends it, and nothing reads h after stop.
func readConsoleKeys(h syscall.Handle) (keys <-chan []byte, resized <-chan struct{}, stop func()) {
	out, sizes := make(chan []byte), make(chan struct{}, 1)
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		defer close(out)
		var records [16]inputRecord
		for {
			select {
			case <-quit:
				return
			default:
			}
			if event, err := syscall.WaitForSingleObject(h, 100); err != nil {
				return
			} else if event != syscall.WAIT_OBJECT_0 {
				continue
			}
			var n uint32
			if r, _, _ := procReadConsoleInputW.Call(uintptr(h), uintptr(unsafe.Pointer(&records[0])), uintptr(len(records)), uintptr(unsafe.Pointer(&n))); r == 0 {
				return
			}
			chars := consoleChars(records[:n], sizes)
			if len(chars) == 0 {
				continue
			}
			select {
			case out <- chars:
			case <-quit:
				return
			}
		}
	}()
	return out, sizes, func() { close(quit); <-done }
}

// consoleChars returns the UTF-8 text of the key presses in records, and
// sends on sizes for a window size event.
func consoleChars(records []inputRecord, sizes chan struct{}) []byte {
	var chars []uint16
	for _, r := range records {
		switch {
		case r.eventType == keyEvent && r.keyDown != 0 && r.char != 0:
			for range max(r.repeat, 1) {
				chars = append(chars, r.char)
			}
		case r.eventType == windowBufferSizeEvent:
			select {
			case sizes <- struct{}{}:
			default:
			}
		}
	}
	return []byte(string(utf16.Decode(chars)))
}
