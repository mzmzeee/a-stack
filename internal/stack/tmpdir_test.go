package stack

import (
	"fmt"
	"io"
	"os"
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
