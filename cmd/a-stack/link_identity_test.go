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
	local.receipt("", local.scoped("session", "close", "--expected-revision", "2", "--operation-id", "close-local-link-fixture")...)

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

func TestLinkedCredentialPinConflictKeepsGuidance(t *testing.T) {
	linkTestHome(t)
	local, remote := newTeam(t), newTeam(t)
	local.receipt("", local.scoped("session", "close", "--expected-revision", "2", "--operation-id", "close-legacy-link-fixture")...)

	lc, err := stack.NewClient(local.state, "", stack.OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}

	rc, err := stack.NewClient(remote.state, "", stack.OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}

	linkTestProfile(t, "team", rc.Discovery.DatabaseID)

	// A private folder: on Windows a plain temporary folder fails the ACL
	// check before the pin check this test is about.
	dir := filepath.Join(t.TempDir(), "private")
	if err = stack.MkdirPrivate(dir); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(dir, "copied-operator.json")

	link := stack.SessionLink{Context: stack.Context{WorkspaceID: remote.workspace, SessionID: remote.session}, Path: local.project, Remote: "team", DatabaseID: rc.Discovery.DatabaseID, CredentialFile: file, Name: "Old shared Session"}
	if _, err = lc.Do(context.Background(), "POST", "/v1/session/link", stack.SessionLinkRequest{Link: link}); err != nil {
		t.Fatal(err)
	}

	t.Chdir(local.project)

	h := home{state: local.state, cwd: local.project}
	for _, scenario := range []string{"legacy", "database", "alias", "offline", "inspect"} {
		t.Run(scenario, func(t *testing.T) {
			credential := rc.Credential
			switch scenario {
			case "database":
				credential.Enrollment = &stack.Enrollment{DatabaseID: shareToken()}
			case "alias":
				credential.Enrollment = &stack.Enrollment{DatabaseID: rc.Discovery.DatabaseID}
				credential.Remote = "old-name"
			}

			if err := stack.ReplacePrivate(file, credential); err != nil {
				t.Fatal(err)
			}

			original := sharingRemoteClient
			if scenario == "offline" {
				sharingRemoteClient = func(context.Context, string, string, stack.CapabilityKind) (*stack.Client, error) {
					return nil, &stack.Error{Code: "storage_unavailable", Message: "offline", Retryable: true}
				}
			} else if scenario == "inspect" {
				// A reachable server lacking the linked Session is not a profile-pin error.
				sharingRemoteClient = func(context.Context, string, string, stack.CapabilityKind) (*stack.Client, error) { return lc, nil }
			}

			t.Cleanup(func() { sharingRemoteClient = original })

			rows, err := h.sessions(context.Background(), lc)
			if err != nil {
				t.Fatal(err)
			}

			found := false
			for _, row := range rows {
				if row.Remote == "" {
					continue
				}

				found = true

				if !row.Unreachable {
					t.Error("failed link became openable")
				}

				if scenario == "offline" || scenario == "inspect" {
					if rowState(row) != "unreachable" || row.LinkError != "" {
						t.Error("ordinary failure mislabeled as re-link")
					}
				} else if rowState(row) != "needs re-link" || !strings.Contains(row.LinkError, "enroll") {
					t.Errorf("lost pin guidance: state=%s error=%q", rowState(row), row.LinkError)
				}
			}

			if !found {
				t.Fatal("linked row missing")
			}

			if scenario == "offline" || scenario == "inspect" {
				return
			}

			for _, explicit := range []bool{false, true} {
				args := []string{"session", "invite", "friend@example.com", "--state-dir", local.state}
				if explicit {
					args = append(args, "--workspace", remote.workspace, "--session", remote.session)
				}

				var out, stderr bytes.Buffer
				if run(args, &out, &stderr) == 0 || !strings.Contains(stderr.String(), "enroll") {
					t.Error("invite lost pin guidance", stderr.String())
				}
			}
		})
	}
}
