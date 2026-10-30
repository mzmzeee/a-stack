package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestCLIShareUnsharePreservesEnrollment(t *testing.T) {
	source := newTeam(t)
	destination := newTeam(t)
	home := hookHome(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("AppData", filepath.Join(home, "config"))
	t.Chdir(source.project)

	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "git@example.com:team/project.git"}} {
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, out)
		}
	}

	var installOut, installErr bytes.Buffer
	if code := run([]string{"hook", "install", "--state-dir", source.state, "--credential-file", source.workerFile, "--claude", "--native-session", "exact-native-session"}, &installOut, &installErr); code != 0 {
		t.Fatal(installErr.String())
	}

	enrollmentPath, err := enrollmentFile(source.worker)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(enrollmentPath)
	if err != nil {
		t.Fatal(err)
	}

	var before hookEnrollment
	if json.Unmarshal(data, &before) != nil {
		t.Fatal("enrollment")
	}

	manifest := filepath.Join(home, "agents.json")
	data, _ = json.Marshal(map[string]string{source.orch: source.orchFile, source.worker: source.workerFile})
	if err = os.WriteFile(manifest, data, 0o600); err != nil {
		t.Fatal(err)
	}

	original := sharingRemoteClient
	sharingRemoteClient = func(_ context.Context, _ string, credential string, _ stack.CapabilityKind) (*stack.Client, error) {
		if credential == "" {
			return nil, invalid("server credential required")
		}

		return stack.NewClient(destination.state, "", stack.OperatorCapability)
	}
	t.Cleanup(func() { sharingRemoteClient = original })

	var out, errout bytes.Buffer
	if code := run([]string{"session", "share", "--state-dir", source.state, "--remote", "team"}, &out, &errout); code == 0 {
		t.Fatal("missing credential should fail preflight")
	}

	journals, err := filepath.Glob(filepath.Join(home, "config", "a-stack", "transfers", "*.json"))
	if err != nil || len(journals) != 0 {
		t.Fatal("failed preflight persisted a journal", err)
	}

	out.Reset()
	errout.Reset()

	args := []string{"session", "share", "--state-dir", source.state, "--remote", "team", "--credential-file", filepath.Join(destination.state, "operator.json"), "--agent-files", manifest}
	if code := run(args, &out, &errout); code != 0 {
		t.Fatalf("share exit %d: %s", code, errout.String())
	}

	local, err := stack.NewClient(source.state, "", stack.OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}

	links, err := sessionLinks(context.Background(), local)
	if err != nil || len(links) != 1 {
		t.Fatal(links, err)
	}

	h := homeForShareTest(source)

	rows, err := h.sessions(context.Background(), local)
	if err != nil || len(rows) != 1 || rows[0].Remote != "team" {
		t.Fatal(rows, err)
	}

	data, err = os.ReadFile(enrollmentPath)
	if err != nil {
		t.Fatal(err)
	}

	var after hookEnrollment
	if json.Unmarshal(data, &after) != nil {
		t.Fatal("enrollment")
	}

	if after.Credential == before.Credential || after.Machine != before.Machine || after.Session != before.Session || after.Revision != before.Revision {
		t.Fatal("exact enrollment was not preserved")
	}

	out.Reset()
	errout.Reset()

	if code := run([]string{"session", "unshare", "--state-dir", source.state, "--agent-files", manifest}, &out, &errout); code != 0 {
		t.Fatalf("unshare exit %d: %s", code, errout.String())
	}

	links, err = sessionLinks(context.Background(), local)
	if err != nil || len(links) != 0 {
		t.Fatal(links, err)
	}

	if _, err = inspectSession(context.Background(), local, stack.Context{WorkspaceID: source.workspace, SessionID: source.session}); err != nil {
		t.Fatal(err)
	}

	data, err = os.ReadFile(enrollmentPath)
	if err != nil {
		t.Fatal(err)
	}

	var restored hookEnrollment
	if json.Unmarshal(data, &restored) != nil || restored.Session != before.Session || restored.Machine != before.Machine || restored.Credential == after.Credential {
		t.Fatal("return enrollment")
	}
}

func homeForShareTest(f *team) *home { return &home{state: f.state, cwd: f.project} }
