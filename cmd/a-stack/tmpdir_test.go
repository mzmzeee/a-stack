package main

import (
	"fmt"
	"os"
	"path/filepath"
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
