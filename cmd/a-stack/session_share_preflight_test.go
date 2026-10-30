package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestTransferJournalExclusiveLock(t *testing.T) {
	source := newTeam(t)
	home := hookHome(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("AppData", filepath.Join(home, "config"))
	t.Chdir(source.project)
	folder, err := stack.ResolvePath(source.project)
	if err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte(folder))
	dir := filepath.Join(home, "config", "a-stack", "transfers", hex.EncodeToString(key[:])+".lock")
	if err = stack.MkdirPrivate(dir); err != nil {
		t.Fatal(err)
	}
	release, ok := tryLock(dir)
	if !ok {
		t.Fatal("fixture lock failed")
	}
	defer release()
	for _, command := range []string{"share", "unshare"} {
		var out, stderr bytes.Buffer
		if run([]string{"session", command, "--state-dir", source.state}, &out, &stderr) == 0 || !strings.Contains(stderr.String(), "another transfer") {
			t.Fatalf("%s did not honor folder lock", command)
		}
	}
}

func TestSessionLinkExplainsLocalService(t *testing.T) {
	home := hookHome(t)
	t.Chdir(home)
	var out, stderr bytes.Buffer
	if run([]string{"session", "link", "team", "unused", "--state-dir", filepath.Join(home, "missing")}, &out, &stderr) == 0 || !strings.Contains(stderr.String(), "service install") {
		t.Fatal("missing actionable local-service guidance")
	}
}

func TestConnectCodeCommand(t *testing.T) {
	source := newTeam(t)
	var out, stderr bytes.Buffer
	if run([]string{"connect-code", "--state-dir", source.state}, &out, &stderr) != 0 {
		t.Fatal(stderr.String())
	}
	var result struct {
		Data struct {
			Code string `json:"code"`
		}
	}
	if json.Unmarshal(out.Bytes(), &result) != nil || len(result.Data.Code) != 22 {
		t.Fatal("missing short owner code")
	}
	// The command does not write a plaintext code file beside the service state.
	entries, err := os.ReadDir(source.state)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "code") {
			t.Fatal("unexpected code file")
		}
	}
}
