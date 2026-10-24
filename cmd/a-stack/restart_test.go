package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestServeReportsATakenLastPortOnStderr(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	first := launch(t, dir)
	data, err := os.ReadFile(filepath.Join(dir, "service.json"))
	var discovery struct {
		URL string `json:"url"`
	}
	if err != nil || json.Unmarshal(data, &discovery) != nil {
		t.Fatal("discovery", err)
	}
	address := strings.TrimPrefix(discovery.URL, "http://")
	if first.Process.Signal(syscall.SIGTERM) != nil || first.Wait() != nil {
		t.Fatal("stop first serve")
	}
	squatter, err := net.Listen("tcp4", address)
	if err != nil {
		t.Fatal(err)
	}
	defer squatter.Close()
	second := child(t, "serve", "--state-dir", dir)
	stdout, err := second.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := second.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = second.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Process.Kill(); _ = second.Wait() })
	notice, _ := bufio.NewReader(stderr).ReadString('\n')
	ready, _ := bufio.NewReader(stdout).ReadString('\n')
	_, oldPort, _ := net.SplitHostPort(address)
	if !strings.Contains(ready, `"ready"`) || !strings.HasPrefix(notice, "port "+oldPort+" from the last run is in use; using port ") || !strings.HasSuffix(notice, ". Open browser tabs need a-stack again.\n") {
		t.Fatalf("stderr %q, stdout %q", notice, ready)
	}
}
