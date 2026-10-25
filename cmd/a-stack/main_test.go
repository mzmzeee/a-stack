package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// Child processes execute the actual CLI entry point, including exit codes and
// signal handling, without installing a service or changing shell settings.
func TestCLIProcess(t *testing.T) {
	if os.Getenv("ASTACK_TEST_CHILD") == "1" {
		for i, a := range os.Args {
			if a == "--" {
				os.Exit(run(os.Args[i+1:], os.Stdout, os.Stderr))
			}
		}
		os.Exit(99)
	}
}
func child(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(exe, append([]string{"-test.run=^TestCLIProcess$", "--"}, args...)...)
	command.Env = append(os.Environ(), "ASTACK_TEST_CHILD=1")
	return command
}
func commandResult(t *testing.T, args []string, wantExit int) ([]byte, []byte) {
	t.Helper()
	cmd := child(t, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exit := 0
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			exit = e.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	if exit != wantExit {
		t.Fatalf("exit=%d want=%d stderr=%s", exit, wantExit, stderr.String())
	}
	if wantExit != 0 {
		if stdout.Len() != 0 || !json.Valid(stderr.Bytes()) {
			t.Fatalf("error streams: stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
	} else if !json.Valid(stdout.Bytes()) || stderr.Len() != 0 {
		t.Fatalf("success streams: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	return stdout.Bytes(), stderr.Bytes()
}
func launch(t *testing.T, dir string) *exec.Cmd {
	t.Helper()
	command := child(t, "serve", "--state-dir", dir, "--json")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	ready, err := bufio.NewReader(stdout).ReadBytes('\n')
	if err != nil || !json.Valid(ready) || !bytes.Contains(ready, []byte(`"ready"`)) || !bytes.Contains(ready, []byte(`"version":"dev"`)) {
		t.Fatalf("not ready: %s %v", ready, err)
	}
	return command
}
func TestCLIRealProcessesCrashRestartBackupRestore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	first := launch(t, dir)
	credential, err := os.ReadFile(filepath.Join(dir, "operator.json"))
	if err != nil {
		t.Fatal(err)
	}
	health, _ := commandResult(t, []string{"health", "--state-dir", dir, "--json"}, 0)
	var h struct {
		Data stack.Health `json:"data"`
	}
	if json.Unmarshal(health, &h) != nil || h.Data.Version != "dev" {
		t.Fatal("health")
	}
	storage, _ := commandResult(t, []string{"--json", "storage", "status", "--state-dir", dir}, 0)
	var st struct {
		Data stack.StorageStatus `json:"data"`
	}
	if json.Unmarshal(storage, &st) != nil || st.Data.FreeBytes == nil || *st.Data.FreeBytes == 0 || st.Data.LowDisk != (*st.Data.FreeBytes < stack.LowDiskBytes) {
		t.Fatal("storage status free space", string(storage))
	}
	// Exit code 3 lets the systemd unit stop instead of restarting in a loop.
	_, stderr := commandResult(t, []string{"serve", "--state-dir", dir, "--json"}, 3)
	if !bytes.Contains(stderr, []byte(`"conflict"`)) || !bytes.Contains(stderr, []byte("already running")) {
		t.Fatal("second start did not conflict")
	}
	commandResult(t, []string{"health", "--as", "agent", "--state-dir", dir, "--json"}, 2)
	commandResult(t, []string{"health", "--state-dir", dir, "--unknown", "--json"}, 2)
	// Abrupt process loss leaves stale discovery and SQLite's WAL. Restart uses
	// SQLite recovery, never deletes a sidecar, and reuses the original credential.
	if first.Process.Kill() != nil {
		t.Fatal("kill")
	}
	_ = first.Wait()
	second := launch(t, dir)
	after, err := os.ReadFile(filepath.Join(dir, "operator.json"))
	if err != nil || !bytes.Equal(credential, after) {
		t.Fatal("crash restart rotated credential")
	}
	health, _ = commandResult(t, []string{"health", "--state-dir", dir, "--json"}, 0)
	var h2 struct {
		Data stack.Health `json:"data"`
	}
	if json.Unmarshal(health, &h2) != nil || h2.Data.DatabaseID != h.Data.DatabaseID {
		t.Fatal("crash restart changed identity")
	}
	backupDir := filepath.Join(t.TempDir(), "backups")
	if os.Mkdir(backupDir, 0700) != nil {
		t.Fatal("backup dir")
	}
	backup := filepath.Join(backupDir, "saved.db")
	commandResult(t, []string{"storage", "backup", "--state-dir", dir, "--destination", backup, "--json"}, 0)
	if second.Process.Signal(syscall.SIGTERM) != nil {
		t.Fatal("terminate")
	}
	if err = second.Wait(); err != nil {
		t.Fatal(err)
	}
	commandResult(t, []string{"health", "--state-dir", dir, "--json"}, 1)
	restoredDir := filepath.Join(t.TempDir(), "restored")
	if os.Mkdir(restoredDir, 0700) != nil {
		t.Fatal("restore dir")
	}
	saved, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"state.db": saved, "operator.json": credential} {
		if os.WriteFile(filepath.Join(restoredDir, name), data, 0600) != nil {
			t.Fatal("copy restored file")
		}
	}
	restored := launch(t, restoredDir)
	health, _ = commandResult(t, []string{"health", "--state-dir", restoredDir, "--json"}, 0)
	if json.Unmarshal(health, &h2) != nil || h2.Data.DatabaseID != h.Data.DatabaseID {
		t.Fatal("restore identity")
	}
	if restored.Process.Signal(syscall.SIGTERM) != nil {
		t.Fatal("terminate restored")
	}
	if err = restored.Wait(); err != nil {
		t.Fatal(err)
	}
}
func TestCLIParsingAndMachineErrors(t *testing.T) {
	for _, args := range [][]string{{"health", "--listen", "127.0.0.1:0"}, {"health", "--state-dir"}, {"serve", "--as", "agent"}, {"storage", "backup", "--state-dir", "relative"}, {"health", "--json=not-bool"}, {"unsupported"}} {
		var out, errout bytes.Buffer
		if run(args, &out, &errout) != 2 || out.Len() != 0 || !json.Valid(errout.Bytes()) {
			t.Fatalf("bad machine error for %s", strings.Join(args, " "))
		}
	}
	commandResult(t, []string{"--help", "--json"}, 0)
	var out, errout bytes.Buffer
	if run([]string{"version"}, &out, &errout) != 0 || strings.TrimSpace(out.String()) != `{"data":{"version":"dev"}}` {
		t.Fatalf("bad version output %q", out.String())
	}
}
