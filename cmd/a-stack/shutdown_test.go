package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The first Ctrl-C waits up to 10 seconds for open requests; a second one ends
// serve at once.
func TestServeSecondInterruptExitsAtOnce(t *testing.T) {
	unixOnly(t, "no SIGINT to another process")
	dir := filepath.Join(t.TempDir(), "state")
	server := launch(t, dir)
	data, err := os.ReadFile(filepath.Join(dir, "service.json"))
	var discovery struct {
		URL string `json:"url"`
	}
	if err != nil || json.Unmarshal(data, &discovery) != nil {
		t.Fatal("discovery", err)
	}
	// A request with unfinished headers keeps the shutdown waiting until the
	// service's 5-second header timeout.
	conn, err := net.Dial("tcp4", strings.TrimPrefix(discovery.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.Write([]byte("GET /v1/health HTTP/1.1\r\n")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	done := make(chan error, 1)
	if err = server.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	go func() { done <- server.Wait() }()
	select {
	case <-done:
		t.Fatal("serve did not wait for the open request")
	case <-time.After(500 * time.Millisecond):
	}
	if err = server.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the second interrupt did not end serve")
	}
	if status, ok := server.ProcessState.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGINT {
		t.Fatalf("serve ended with %v, want the second SIGINT", server.ProcessState)
	}
}
