package stack

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveJunction(t *testing.T) {
	root := privateTestDir(t)
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(target, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(root, "junction")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %s: %v", out, err)
	}
	defer os.Remove(junction)
	resolved, err := ResolvePath(filepath.Join(junction, "file"))
	want, wantErr := ResolvePath(file)
	if err != nil || wantErr != nil || !strings.EqualFold(resolved, want) {
		t.Fatal(resolved, want, err, wantErr)
	}
	if err := os.Mkdir(filepath.Join(target, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := statePath(filepath.Join(junction, "missing-state")); err == nil {
		t.Fatal("junction bypassed checkout guard")
	}
}
