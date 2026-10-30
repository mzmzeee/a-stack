package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestHomeMixedSlowHealthyLinks(t *testing.T) {
	local, remote := newTeam(t), newTeam(t)
	client, err := stack.NewClient(local.state, "", stack.OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}

	remoteClient, err := stack.NewClient(remote.state, "", stack.OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"a-slow", "b-healthy"} {
		ref := stack.Context{WorkspaceID: remote.workspace, SessionID: remote.session}
		if name == "a-slow" {
			ref.SessionID = local.session
		}

		_, err = client.Do(context.Background(), "POST", "/v1/session/link", stack.SessionLinkRequest{Link: stack.SessionLink{Context: ref, Path: remote.project, Remote: name, DatabaseID: remoteClient.Discovery.DatabaseID, CredentialFile: filepath.Join(remote.state, "operator.json"), Name: name}})
		if err != nil {
			t.Fatal(err)
		}
	}

	original := sharingRemoteClient
	sharingRemoteClient = func(ctx context.Context, name, file string, kind stack.CapabilityKind) (*stack.Client, error) {
		if name == "a-slow" {
			<-ctx.Done()
			return nil, ctx.Err()
		}

		return remoteClient, nil
	}
	t.Cleanup(func() { sharingRemoteClient = original })
	var out bytes.Buffer
	h := &home{state: local.state, cwd: remote.project, stdout: &out, machine: true, noBrowser: true}
	start := time.Now()

	rows, err := h.sessions(context.Background(), client)
	if err != nil {
		t.Fatal(err)
	}

	if time.Since(start) > 3*time.Second {
		t.Fatal("mixed listing exceeded deadline")
	}

	for _, row := range rows {
		if row.Remote == "b-healthy" && rowState(row) != "open" {
			t.Error("hanging link poisoned healthy link")
		}
	}

	local.receipt("", local.scoped("session", "close", "--expected-revision", "2", "--operation-id", "mixed-close-local")...)

	if err = h.last(context.Background(), client); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), remote.session) {
		t.Fatal("bare shortcut did not open healthy remote")
	}

	out.Reset()

	if err = h.resumeSession(context.Background(), client); err != nil {
		t.Fatal(err)
	}

	var result struct {
		Data struct {
			Sessions []struct {
				Remote      string `json:"remote"`
				Unreachable bool   `json:"unreachable"`
			} `json:"sessions"`
		} `json:"data"`
	}
	if err = json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}

	for _, row := range result.Data.Sessions {
		if row.Remote == "b-healthy" && row.Unreachable {
			t.Error("resume marked healthy remote unreachable")
		}
	}
}

func TestLinkedSummaryUnknown(t *testing.T) {
	// A remote Session timestamp is not complete Task activity and must not rank as such.
	remote := sessionRow{Remote: "team", SessionID: "a", ChangedAt: time.Now().Add(time.Hour)}
	local := sessionRow{SessionID: "b", ChangedAt: time.Now()}
	rows := []sessionRow{remote, local}
	sortRows(rows)

	if rows[0].SessionID != local.SessionID {
		t.Error("incomplete remote activity ranked ahead of known activity")
	}

	if !strings.Contains(rowCounts(remote), "unknown") {
		t.Error("picker reports unknown counts as zero")
	}

	encoded, err := json.Marshal(remote)
	if err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{`"tasks":null`, `"needs_you":null`, `"changed_at":null`} {
		if !bytes.Contains(encoded, []byte(field)) {
			t.Errorf("remote summary missing %s: %s", field, encoded)
		}
	}

	if !strings.HasSuffix(formatRow(remote, false, "", time.Now(), 120), "?") {
		t.Error("picker reports incomplete activity as an age")
	}
}
