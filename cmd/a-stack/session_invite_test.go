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

func inviteTeam(t *testing.T, shared bool) *team {
	t.Helper()
	root := t.TempDir()

	f := &team{t: t, state: filepath.Join(root, "state"), project: filepath.Join(root, "project")}
	service, err := stack.Start(context.Background(), f.state, "")
	if err != nil {
		t.Fatal(err)
	}

	if shared {
		service.SetServeHost("sharing.example.ts.net")
	}

	done := make(chan error, 1)
	go func() { done <- service.Serve() }()
	t.Cleanup(func() { _ = service.Close(context.Background()); <-done })

	if err = os.Mkdir(f.project, 0o700); err != nil {
		t.Fatal(err)
	}

	f.project, _ = filepath.EvalSymlinks(f.project)
	f.workspace = f.receipt("", "workspace", "create", "--path", f.project, "--operation-id", "invite-workspace").ResourceID
	f.session = f.receipt("", "session", "create", "--workspace", f.workspace, "--name", "Build", "--operation-id", "invite-build").ResourceID

	return f
}

func TestInviteSelectsUnambiguouslyAndNamesSession(t *testing.T) {
	linkTestHome(t)
	f := inviteTeam(t, true)
	t.Chdir(f.project)
	other := f.receipt("", "session", "create", "--workspace", f.workspace, "--name", "Other", "--operation-id", "invite-other").ResourceID

	var out, stderr bytes.Buffer
	args := []string{"session", "invite", "friend@example.com", "--state-dir", f.state}
	if run(args, &out, &stderr) == 0 || out.Len() != 0 {
		t.Error("ambiguous invitation issued")
	}

	for _, want := range []string{"--session", "Build", f.session, "Other", other} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("ambiguity did not list %s", want)
		}
	}

	out.Reset()
	stderr.Reset()

	if run(append(args, "--session", f.session), &out, &stderr) != 0 {
		t.Fatal(stderr.String())
	}

	if json.Valid(out.Bytes()) || !strings.Contains(out.String(), "Build") || !strings.Contains(out.String(), f.session) || !strings.Contains(out.String(), "Code:") {
		t.Error("default output must name Session and code as text")
	}

	out.Reset()
	stderr.Reset()

	if run(append(args, "--session", other, "--json"), &out, &stderr) != 0 {
		t.Fatal(stderr.String())
	}

	var result struct {
		Data struct {
			SessionID   string `json:"session_id"`
			WorkspaceID string `json:"workspace_id"`
			Name        string `json:"name"`
			Code        string `json:"code"`
		}
	}
	if json.Unmarshal(out.Bytes(), &result) != nil || result.Data.SessionID != other || result.Data.WorkspaceID != f.workspace || result.Data.Name != "Other" || len(result.Data.Code) != 22 {
		t.Error("JSON lacks invitation target or code")
	}

	f.receipt("", "session", "close", "--workspace", f.workspace, "--session", other, "--expected-revision", "1", "--operation-id", "invite-close-other")
	// Descendants use the nearest Workspace, just like bare a-stack.
	sub := filepath.Join(f.project, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}

	t.Chdir(sub)
	out.Reset()
	stderr.Reset()

	if run(args, &out, &stderr) != 0 || !strings.Contains(out.String(), f.session) {
		t.Fatal("single parent Session not selected", stderr.String())
	}

	nested := f.receipt("", "workspace", "create", "--path", sub, "--operation-id", "invite-nested").ResourceID
	nestedSession := f.receipt("", "session", "create", "--workspace", nested, "--name", "Nested", "--operation-id", "invite-nested-session").ResourceID
	out.Reset()
	stderr.Reset()

	if run(args, &out, &stderr) != 0 || !strings.Contains(out.String(), nestedSession) || strings.Contains(out.String(), f.session) {
		t.Fatal("nearest Workspace not selected", stderr.String())
	}

	f.receipt("", "session", "close", "--workspace", nested, "--session", nestedSession, "--expected-revision", "1", "--operation-id", "invite-close-nested")
	out.Reset()
	stderr.Reset()

	if run(args, &out, &stderr) == 0 || !strings.Contains(stderr.String(), "no open") {
		t.Error("closed nearest Workspace fell back to parent")
	}
}

func TestInviteRefusesLocalOnlyService(t *testing.T) {
	linkTestHome(t)
	f := inviteTeam(t, false)
	t.Chdir(f.project)

	for _, explicit := range []bool{false, true} {
		args := []string{"session", "invite", "friend@example.com", "--state-dir", f.state}
		if explicit {
			args = append(args, "--workspace", f.workspace, "--session", f.session)
		}

		var out, stderr bytes.Buffer
		if run(args, &out, &stderr) == 0 || out.Len() != 0 || !strings.Contains(stderr.String(), "session share") {
			t.Error("local-only invite lacked sharing guidance", stderr.String())
		}
	}

	client, err := stack.NewClient(f.state, "", stack.OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}

	data, err := client.Do(context.Background(), "POST", "/v1/session/transfer/export", stack.TransferExportRequest{Context: stack.Context{WorkspaceID: f.workspace, SessionID: f.session}, TransferID: shareToken(), Repository: "example.com/repo"})
	if err != nil {
		t.Fatal(err)
	}

	bundle, err := decodeData[stack.TransferBundle](data)
	if err != nil {
		t.Fatal(err)
	}

	for _, table := range bundle.Tables {
		if table.Name == "session_invites" && len(table.Rows) != 0 {
			t.Fatal("local-only refusal created an invitation")
		}
	}
}

func TestInviteLocalAndLinkedAreAmbiguous(t *testing.T) {
	linkTestHome(t)
	local, remote := inviteTeam(t, true), inviteTeam(t, true)
	t.Chdir(local.project)

	lc, err := stack.NewClient(local.state, "", stack.OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}

	rc, err := stack.NewClient(remote.state, "", stack.OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}

	rc.RemoteName = "team"
	linkTestProfile(t, "team", rc.Discovery.DatabaseID)

	_, err = lc.Do(context.Background(), "POST", "/v1/session/link", stack.SessionLinkRequest{Link: stack.SessionLink{Context: stack.Context{WorkspaceID: remote.workspace, SessionID: remote.session}, Path: local.project, Remote: "team", DatabaseID: rc.Discovery.DatabaseID, CredentialFile: filepath.Join(remote.state, "operator.json"), Name: "Remote Build"}})
	if err != nil {
		t.Fatal(err)
	}

	original := sharingRemoteClient
	sharingRemoteClient = func(context.Context, string, string, stack.CapabilityKind) (*stack.Client, error) { return rc, nil }
	t.Cleanup(func() { sharingRemoteClient = original })

	var out, stderr bytes.Buffer
	args := []string{"session", "invite", "friend@example.com", "--state-dir", local.state}
	if run(args, &out, &stderr) == 0 || !strings.Contains(stderr.String(), local.session) || !strings.Contains(stderr.String(), remote.session) {
		t.Error("local/linked ambiguity not refused", stderr.String())
	}

	for _, contextFlags := range [][]string{{"--session", remote.session}, {"--workspace", remote.workspace, "--session", remote.session}} {
		out.Reset()
		stderr.Reset()

		if run(append(args, contextFlags...), &out, &stderr) != 0 || !strings.Contains(out.String(), remote.session) {
			t.Error("explicit linked Session not selected", stderr.String())
		}
	}
}
