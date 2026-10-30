package stack

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"testing"
)

// The checkout guard rejects state below any .git, so tests that keep state in
// TMPDIR cannot pass there. Say so once instead of failing dozens of tests.
func TestMain(m *testing.M) {
	if _, err := statePath(os.TempDir()); err != nil {
		fmt.Fprintf(os.Stderr, "TMPDIR is inside a Git checkout: %s\n", os.TempDir())
		os.Exit(1)
	}

	storageLogOutput = io.Discard // TestStorageError* capture it
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
