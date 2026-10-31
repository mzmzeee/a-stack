package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func linkTestHome(t *testing.T) {
	t.Helper()
	root := hookHome(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("AppData", filepath.Join(root, "config"))
}

func linkTestProfile(t *testing.T, name, database string) {
	t.Helper()

	root, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(root, "a-stack", "remotes")
	if err = stack.MkdirPrivate(dir); err != nil {
		t.Fatal(err)
	}

	if err = stack.ReplacePrivate(filepath.Join(dir, name+".json"), stack.RemoteProfile{Name: name, URL: "https://example.invalid", DatabaseID: database, Version: "test"}); err != nil {
		t.Fatal(err)
	}
}

func TestStaleLinkNeverProbesReusedProfile(t *testing.T) {
	linkTestHome(t)

	local, remote := newTeam(t), newTeam(t)
	client, err := stack.NewClient(local.state, "", stack.OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}

	remoteClient, err := stack.NewClient(remote.state, "", stack.OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}

	linkTestProfile(t, "team", remoteClient.Discovery.DatabaseID)
	t.Chdir(local.project)

	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "git@example.com:team/repo.git"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}

	link := stack.SessionLink{Context: stack.Context{WorkspaceID: remote.workspace, SessionID: remote.session}, Path: local.project, Remote: "team", DatabaseID: client.Discovery.DatabaseID, CredentialFile: filepath.Join(local.state, "operator.json"), Name: "Old server"}
	if _, err = client.Do(context.Background(), "POST", "/v1/session/link", stack.SessionLinkRequest{Link: link}); err != nil {
		t.Fatal(err)
	}

	original := sharingRemoteClient
	calls := 0
	sharingRemoteClient = func(context.Context, string, string, stack.CapabilityKind) (*stack.Client, error) {
		calls++
		return nil, invalid("remote contacted")
	}
	t.Cleanup(func() { sharingRemoteClient = original })
	h := &home{state: local.state, cwd: local.project}

	rows, err := h.sessions(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}

	for _, row := range rows {
		if row.Remote != "" {
			if rowState(row) != "needs re-link" {
				t.Errorf("stale link state=%q", rowState(row))
			}

			if err = h.openWith(context.Background(), client, row, map[string]any{}, nil, nil); err == nil || !strings.Contains(err.Error(), "re-link") {
				t.Error("open missed relink guidance", err)
			}
		}
	}

	for _, command := range []string{"invite", "unshare"} {
		args := []string{"session", command, "--state-dir", local.state}
		if command == "invite" {
			args = append(args, "friend@example.com")
		}

		var out, stderr bytes.Buffer
		if run(args, &out, &stderr) == 0 || !strings.Contains(stderr.String(), "re-link") {
			t.Error(command, "missed relink guidance", stderr.String())
		}
	}

	if calls != 0 {
		t.Fatal("stale link reached remote client", calls)
	}

	// Re-linking leaves the older recovery link visible, but must make folder commands usable.
	fresh := link

	fresh.DatabaseID = remoteClient.Discovery.DatabaseID
	fresh.Name = "Z fresh link"
	if _, err = client.Do(context.Background(), "POST", "/v1/session/link", stack.SessionLinkRequest{Link: fresh}); err != nil {
		t.Fatal(err)
	}

	sharingRemoteClient = func(context.Context, string, string, stack.CapabilityKind) (*stack.Client, error) {
		calls++
		return remoteClient, nil
	}

	var out, stderr bytes.Buffer
	if run([]string{"session", "invite", "next@example.com", "--state-dir", local.state}, &out, &stderr) != 0 {
		t.Fatal("fresh link did not bypass stale link", stderr.String())
	}

	out.Reset()
	stderr.Reset()

	_ = run([]string{"session", "unshare", "--state-dir", local.state}, &out, &stderr)
	if strings.Contains(stderr.String(), "re-link") {
		t.Fatal("unshare still selected stale link")
	}
}
