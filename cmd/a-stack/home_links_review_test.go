package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
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

func TestHomeSaturatedProbes(t *testing.T) {
	local, remote := newTeam(t), newTeam(t)
	client, err := stack.NewClient(local.state, "", stack.OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}

	rc, err := stack.NewClient(remote.state, "", stack.OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 9; i++ {
		ref := stack.Context{WorkspaceID: remote.workspace, SessionID: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(i + 1)}, 32))}

		name := fmt.Sprintf("a-slow-%d", i)
		path := local.project
		if i == 8 {
			name = "z-healthy"
			ref.SessionID = remote.session
			path = remote.project
		}

		_, err = client.Do(context.Background(), "POST", "/v1/session/link", stack.SessionLinkRequest{Link: stack.SessionLink{Context: ref, Path: path, Remote: name, DatabaseID: rc.Discovery.DatabaseID, CredentialFile: filepath.Join(remote.state, "operator.json"), Name: name}})
		if err != nil {
			t.Fatal(err)
		}
	}

	var active, peak atomic.Int32
	original := sharingRemoteClient
	sharingRemoteClient = func(ctx context.Context, name, file string, kind stack.CapabilityKind) (*stack.Client, error) {
		n := active.Add(1)
		defer active.Add(-1)

		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}

		if name != "z-healthy" {
			<-ctx.Done()
			return nil, ctx.Err()
		}

		return rc, nil
	}
	t.Cleanup(func() { sharingRemoteClient = original })
	// Slow links get priority here: the healthy ninth must still get a turn.
	h := &home{state: local.state, cwd: local.project}
	for range 2 {
		start := time.Now()

		rows, err := h.sessions(context.Background(), client)
		if err != nil {
			t.Fatal(err)
		}

		found := false
		for _, row := range rows {
			if row.Remote == "z-healthy" {
				found = true

				if row.Unreachable {
					t.Error("eight hung links starved healthy ninth")
				}
			}
		}

		if !found || time.Since(start) > 3*time.Second || active.Load() != 0 || peak.Load() > 8 {
			t.Fatalf("probe bounds: found=%v active=%d peak=%d", found, active.Load(), peak.Load())
		}
	}

	// With less than one slow probe's budget, only current-folder priority can reach the healthy link.
	h.cwd = remote.project
	short, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()

	rows, err := h.sessions(short, client)
	if err != nil {
		t.Fatal(err)
	}

	for _, row := range rows {
		if row.Remote == "z-healthy" && row.Unreachable {
			t.Fatal("current-folder link was not prioritized")
		}
	}
}

func TestHomeHealthyLatency(t *testing.T) {
	for _, count := range []int{1, 9} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			local, remote := newTeam(t), newTeam(t)
			client, err := stack.NewClient(local.state, "", stack.OperatorCapability)
			if err != nil {
				t.Fatal(err)
			}

			rc, err := stack.NewClient(remote.state, "", stack.OperatorCapability)
			if err != nil {
				t.Fatal(err)
			}

			for i := 0; i < count; i++ {
				ref := stack.Context{WorkspaceID: remote.workspace, SessionID: remote.session}
				name := "a-healthy"
				if i > 0 {
					name = fmt.Sprintf("other-%d", i)
					ref.SessionID = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(i)}, 32))
				}

				_, err = client.Do(context.Background(), "POST", "/v1/session/link", stack.SessionLinkRequest{Link: stack.SessionLink{Context: ref, Path: remote.project, Remote: name, DatabaseID: rc.Discovery.DatabaseID, CredentialFile: filepath.Join(remote.state, "operator.json"), Name: name}})
				if err != nil {
					t.Fatal(err)
				}
			}

			original := sharingRemoteClient
			sharingRemoteClient = func(ctx context.Context, name, file string, kind stack.CapabilityKind) (*stack.Client, error) {
				if name != "a-healthy" {
					return nil, invalid("missing profile")
				}

				timer := time.NewTimer(700 * time.Millisecond)
				defer timer.Stop()

				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-timer.C:
					return rc, nil
				}
			}
			t.Cleanup(func() { sharingRemoteClient = original })
			var out bytes.Buffer
			h := &home{state: local.state, cwd: remote.project, stdout: &out, machine: true, noBrowser: true}
			start := time.Now()

			rows, err := h.sessions(context.Background(), client)
			if err != nil {
				t.Fatal(err)
			}

			for _, row := range rows {
				if row.Remote == "a-healthy" && row.Unreachable {
					t.Error("healthy 700ms link was cut off with time left")
				}
			}

			if time.Since(start) > 3*time.Second {
				t.Fatal("listing exceeded overall deadline")
			}

			if err = h.last(context.Background(), client); err != nil {
				t.Fatal(err)
			}

			if !strings.Contains(out.String(), remote.session) {
				t.Fatal("bare shortcut excluded slow healthy remote")
			}
		})
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
