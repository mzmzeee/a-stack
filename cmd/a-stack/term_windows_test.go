package main

import (
	"testing"
	"unsafe"
)

func TestConsoleChars(t *testing.T) {
	if size := unsafe.Sizeof(inputRecord{}); size != 20 {
		t.Fatalf("inputRecord has %d bytes; INPUT_RECORD has 20", size)
	}
	key := func(c uint16) inputRecord { return inputRecord{eventType: keyEvent, keyDown: 1, char: c} }
	up := key('x')
	up.keyDown = 0
	repeated := key('j')
	repeated.repeat = 2
	sizes := make(chan struct{}, 1)
	records := []inputRecord{key(0x1b), key('['), key('A'), up, {eventType: windowBufferSizeEvent}, repeated, key(0xd83d), key(0xde00), {eventType: keyEvent, keyDown: 1}}
	if got := string(consoleChars(records, sizes)); got != "\x1b[Ajj\U0001F600" {
		t.Fatalf("consoleChars = %q", got)
	}
	select {
	case <-sizes:
	default:
		t.Fatal("no resize for a window size event")
	}
}
