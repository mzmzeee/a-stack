//go:build linux || darwin

package stack

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestFIFOCredentialFailsWithoutBlocking(t *testing.T) {
	path := filepath.Join(privateTestDir(t), "fifo.json")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := ReadCredential(path); done <- err }()
	select {
	case err := <-done:
		requireCode(t, err, "invalid_input")
	case <-time.After(time.Second):
		t.Fatal("FIFO credential blocked")
	}
}
