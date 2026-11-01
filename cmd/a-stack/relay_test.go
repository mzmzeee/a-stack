package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestRelayFakeHerdr(t *testing.T) {
	split := -1
	for i, arg := range os.Args {
		if arg == "--" {
			split = i
			break
		}
	}

	if split < 0 {
		return
	}

	dir := os.Args[split+1]

	args := os.Args[split+2:]
	if len(args) < 2 {
		os.Exit(2)
	}

	if args[0] == "status" {
		os.Stdout.WriteString(`{"running":true,"protocol":22,"compatible":true,"endpoint_compatible":true}`)
		os.Exit(0)
	}

	if args[0] != "agent" {
		os.Exit(2)
	}

	if args[1] == "prompt" {
		f, err := os.OpenFile(filepath.Join(dir, "prompts"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(3)
		}

		json.NewEncoder(f).Encode(args)
		f.Close()
	}

	if data, err := os.ReadFile(filepath.Join(dir, "metadata.json")); err == nil {
		os.Stdout.Write(data)
		os.Exit(0)
	}

	os.Stdout.WriteString(`{"result":{"agent":{"name":"relay-worker","pane_id":"w1:p1","terminal_id":"terminal-1","agent":"codex","agent_status":"idle","agent_session":{"agent":"codex","kind":"id","source":"herdr:codex","value":"native-1"}}}}`)
	os.Exit(0)
}

func relayCLIFixture(t *testing.T) (*team, string, string) {
	t.Helper()

	if !herdrSupported {
		t.Skip("Herdr unsupported")
	}

	root := mailWaitPrivate(t)
	f := newTeam(t)
	f.taskChange(f.orchFile, "relay-assign-orch", 2, "assign", "--agent", f.orch)
	t.Chdir(f.project)

	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	exe := filepath.Join(root, "fake-herdr")
	if err = os.WriteFile(exe, []byte("#!/bin/sh\nGORACE=atexit_sleep_ms=0 exec "+shellQuote(binary)+" -test.run=^TestRelayFakeHerdr$ -- "+shellQuote(root)+" \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err = os.WriteFile(filepath.Join(root, "codex"), []byte("#!/bin/sh\nprintf 'codex 0.164.0\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(root, "local.sock"))
	bind := f.scoped("herdr", "bind", "--agent", f.worker, "--expected-revision", "1", "--operation-id", "relay-enroll-test", "--relay", "--notices", "pane", "--herdr-executable", exe)
	f.receipt(f.workerFile, bind...)
	f.receipt(f.workerFile, bind...)
	f.receipt(f.orchFile, f.scoped("mail", "send", "--task", f.task, "--body", "@02-worker wake", "--operation-id", "relay-mail-test")...)

	return f, root, exe
}

func TestRelayCLIEnrollmentAndPrompt(t *testing.T) {
	f, root, exe := relayCLIFixture(t)
	file := filepath.Join(root, "relay.json")
	if err := stack.ReplacePrivate(file, relayConfig{HerdrExecutable: exe, Credentials: []string{f.workerFile}}); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		var out, errs bytes.Buffer
		if code := run([]string{"relay", "--config", file, "--once"}, &out, &errs); code != 0 {
			t.Fatalf("exit %d %s", code, errs.String())
		}
	}

	prompts, err := os.ReadFile(filepath.Join(root, "prompts"))
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Count(prompts, []byte("\n")) != 1 || bytes.Contains(prompts, []byte("wake")) {
		t.Fatal("duplicate or mail-body prompt")
	}

	enrollmentPath, _ := relayEnrollmentFile(f.workerFile)
	var e relayEnrollment
	if err = stack.ReadRelayFile(enrollmentPath, &e); err != nil {
		t.Fatal(err)
	}

	dir, _ := relayIntervalDir(e)
	if err = os.WriteFile(filepath.Join(dir, "interval.json"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}

	if relayAttempt(context.Background(), exe, f.workerFile) == nil {
		t.Fatal("corrupt interval accepted")
	}
}

func TestRelayCLIRefusesWindowsBeforeIO(t *testing.T) {
	old := herdrSupported
	herdrSupported = false
	t.Cleanup(func() { herdrSupported = old })
	var out, errout bytes.Buffer

	code := run([]string{"relay", "--config", "/absent/config", "--once"}, &out, &errout)
	var e struct {
		Error stack.Error `json:"error"`
	}
	if code != 2 || json.Unmarshal(errout.Bytes(), &e) != nil || !strings.Contains(e.Error.Message, "not supported on Windows") {
		t.Fatalf("%d %s", code, errout.String())
	}
}
