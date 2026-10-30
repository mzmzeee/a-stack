package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

// The checkout guard rejects state below any .git, so tests that keep state in
// TMPDIR cannot pass there. Say so once instead of failing dozens of tests.
func TestMain(m *testing.M) {
	tmp := os.TempDir()
	if resolved, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = resolved
	}

	for dir := tmp; ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			fmt.Fprintf(os.Stderr, "TMPDIR is inside a Git checkout: %s\n", os.TempDir())
			os.Exit(1)
		}

		if filepath.Dir(dir) == dir {
			break
		}
	}

	os.Exit(m.Run())
}

// posixModes is false on Windows, where Go shows no owner-only file modes
// and a-stack protects files with access lists instead.
const posixModes = runtime.GOOS != "windows"

// unixOnly skips a test on Windows.
func unixOnly(t *testing.T, why string) {
	t.Helper()

	if !posixModes {
		t.Skip("not on Windows: " + why)
	}
}

// stopServe stops a serve child with sig and checks that it exits cleanly.
// Windows sends no signal to another process, so there it kills the child.
func stopServe(t *testing.T, command *exec.Cmd, sig syscall.Signal) {
	t.Helper()

	if !posixModes {
		_ = command.Process.Kill()
		_ = command.Wait()
		return
	}

	if command.Process.Signal(sig) != nil {
		t.Fatal("signal")
	}

	if err := command.Wait(); err != nil {
		t.Fatal("stop:", err)
	}
}
