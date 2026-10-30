package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestCLIHerdrRefusedOnWindows(t *testing.T) {
	old := herdrSupported
	herdrSupported = false
	t.Cleanup(func() { herdrSupported = old })

	dir := filepath.Join(t.TempDir(), "state")
	for _, args := range [][]string{
		{"herdr", "focus"},
		{"serve", "--herdr-executable", "herdr"},
	} {
		var stdout, stderr bytes.Buffer

		code := run(append(args, "--state-dir", dir), &stdout, &stderr)
		var failure struct {
			Error stack.Error `json:"error"`
		}
		if code != 2 || json.Unmarshal(stderr.Bytes(), &failure) != nil || !strings.Contains(failure.Error.Message, "not supported on Windows") {
			t.Fatalf("%v: exit %d, %s", args, code, stderr.String())
		}
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("a refused command made the state directory")
	}
}

func TestCLIHerdrBindAndFocusRefusals(t *testing.T) {
	if !herdrSupported {
		t.Skip("Herdr is not supported here")
	}

	// Never reach a real Herdr: no pane variables and a herdr that is absent.
	for _, name := range []string{"HERDR_ENV", "HERDR_PANE_ID", "HERDR_SOCKET_PATH"} {
		t.Setenv(name, "")
	}

	absent := filepath.Join(t.TempDir(), "absent-herdr")
	dir := filepath.Join(t.TempDir(), "state")
	launch(t, dir)

	private := filepath.Join(t.TempDir(), "credentials")
	if stack.MkdirPrivate(private) != nil {
		t.Fatal("private credential dir")
	}

	common := func(args ...string) []string { return append(args, "--state-dir", dir, "--json") }
	w := receiptResult(t, common("workspace", "create", "--operation-id", "herdr-workspace", "--path", t.TempDir()))
	s := receiptResult(t, common("session", "create", "--operation-id", "herdr-session", "--workspace", w.ResourceID, "--name", "Herdr"))
	ref := []string{"--workspace", w.ResourceID, "--session", s.ResourceID}
	file := filepath.Join(private, "worker.json")
	worker := receiptResult(t, common(append([]string{"agent", "register", "--name", "02-worker", "--new-credential-file", file, "--operation-id", "herdr-register"}, ref...)...))
	as := []string{"--as", "agent", "--credential-file", file}
	bind := append(append([]string{"herdr", "bind", "--agent", worker.ResourceID, "--expected-revision", "1", "--operation-id", "herdr-bind-1", "--herdr-executable", absent}, ref...), as...)

	_, stderr := commandResult(t, common(bind...), 2)
	var failure struct {
		Error stack.Error `json:"error"`
	}
	if json.Unmarshal(stderr, &failure) != nil || failure.Error.Code != "invalid_input" {
		t.Fatal("bind outside a Herdr pane", string(stderr))
	}

	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(t.TempDir(), "herdr.sock"))

	_, stderr = commandResult(t, common(bind...), 1)
	if json.Unmarshal(stderr, &failure) != nil || failure.Error.Code != "integration_unavailable" {
		t.Fatal("bind without Herdr", string(stderr))
	}

	focus := append([]string{"herdr", "focus", "--agent", worker.ResourceID}, ref...)

	_, stderr = commandResult(t, common(focus...), 1)
	if json.Unmarshal(stderr, &failure) != nil || failure.Error.Code != "integration_unavailable" {
		t.Fatal("focus without --herdr-executable", string(stderr))
	}

	_, stderr = commandResult(t, common(append(focus, as...)...), 1)
	if json.Unmarshal(stderr, &failure) != nil || failure.Error.Code != "forbidden" {
		t.Fatal("Agent focus", string(stderr))
	}

	commandResult(t, common(append(focus, "--herdr-executable", absent)...), 2)
	commandResult(t, common("herdr", "focus", ref[0], ref[1], ref[2], ref[3]), 2)
}
