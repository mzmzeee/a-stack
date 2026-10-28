package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// Also runs unchanged on the first-round commit, which leaves legacy hooks
// disabled after refresh and has no self-enrollment command.
func TestHookUpgradeAndSelfEnrollmentRegression(t *testing.T) {
	f := newTeam(t)
	hookHome(t)
	t.Chdir(f.project)
	_, errout, code := f.run("", "hook", "install", "--claude", "--credential-file", f.workerFile)
	if code != 0 {
		t.Fatal(errout)
	}
	configs, err := hookSettings(hookFlags{claude: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	file := configs["claude"]
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.ReplaceAll(data, []byte(" --harness claude"), nil)
	if err = os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = refreshInstalledFiles(false, &out); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(after, []byte(" --harness claude")) || !strings.Contains(out.String(), "enroll") {
		t.Fatal("legacy hook upgrade did not migrate and explain enrollment")
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "current-native")
	_, errout, code = f.run(f.workerFile, "hook", "enroll", "--claude")
	if code != 0 {
		t.Fatalf("Agent cannot enroll itself: %s", errout)
	}
}
