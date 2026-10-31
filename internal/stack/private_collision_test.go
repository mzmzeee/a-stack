package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateCollisionExplainsPath(t *testing.T) {
	path := filepath.Join(privateTestDir(t), "profile.json")
	if err := writePrivate(path, map[string]string{"name": "original"}, false); err != nil {
		t.Fatal(err)
	}

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	err = writePrivate(path, map[string]string{"name": "replacement"}, false)
	requireCode(t, err, "conflict")

	if !strings.Contains(err.Error(), path) {
		t.Fatal("collision does not identify the existing file", err)
	}

	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("collision changed original file", err)
	}
}
