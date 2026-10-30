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

func TestHomeLinkedSessionState(t *testing.T) {
	for _, state := range []string{"open", "closed", "unreachable", "inspect-denied"} {
		t.Run(state, func(t *testing.T) {
			local, remote := newTeam(t), newTeam(t)
			client, err := stack.NewClient(local.state, "", stack.OperatorCapability)
			if err != nil {
				t.Fatal(err)
			}

			remoteClient, err := stack.NewClient(remote.state, "", stack.OperatorCapability)
			if err != nil {
				t.Fatal(err)
			}

			if state == "closed" {
				remote.receipt("", remote.scoped("session", "close", "--expected-revision", "2", "--operation-id", "close-linked-session")...)
			}

			want := state
			if state == "inspect-denied" {
				remoteClient.Credential.Token = "invalid"
				want = "unreachable"
			}

			credential := filepath.Join(remote.state, "operator.json")
			ref := stack.Context{WorkspaceID: remote.workspace, SessionID: remote.session}

			_, err = client.Do(context.Background(), "POST", "/v1/session/link", stack.SessionLinkRequest{Link: stack.SessionLink{Context: ref, Path: remote.project, Remote: "linked", DatabaseID: remoteClient.Discovery.DatabaseID, CredentialFile: credential, Name: "Linked"}})
			if err != nil {
				t.Fatal(err)
			}

			wantCalls := 1
			if state == "unreachable" {
				// Two offline links must share one deadline, not add two delays.
				ref.SessionID = local.session

				_, err = client.Do(context.Background(), "POST", "/v1/session/link", stack.SessionLinkRequest{Link: stack.SessionLink{Context: ref, Path: remote.project, Remote: "linked", DatabaseID: remoteClient.Discovery.DatabaseID, CredentialFile: credential, Name: "Second offline link"}})
				if err != nil {
					t.Fatal(err)
				}

				wantCalls = 2
			}

			original := sharingRemoteClient
			calls := 0
			sharingRemoteClient = func(ctx context.Context, name, file string, kind stack.CapabilityKind) (*stack.Client, error) {
				calls++

				if name != "linked" || file != credential || kind != stack.OperatorCapability {
					t.Error("wrong link identity")
				}

				if state == "unreachable" {
					<-ctx.Done()
					return nil, ctx.Err()
				}

				return remoteClient, nil
			}
			t.Cleanup(func() { sharingRemoteClient = original })
			var out bytes.Buffer
			h := &home{state: local.state, cwd: remote.project, stdout: &out, machine: true, noBrowser: true}
			started := time.Now()

			rows, err := h.sessions(context.Background(), client)
			if err != nil {
				t.Fatal(err)
			}

			if time.Since(started) > 3*time.Second {
				t.Fatal("listing exceeded remote deadline")
			}

			found := false
			for _, row := range rows {
				if row.SessionID != remote.session {
					continue
				}

				found = true

				if got := rowState(row); got != want {
					t.Errorf("linked state = %q, want %q", got, want)
				}

				encoded, _ := json.Marshal(row)
				if want == "unreachable" && !bytes.Contains(encoded, []byte(`"unreachable":true`)) {
					t.Error("JSON must mark unknown remote state")
				}
			}

			if !found || calls != wantCalls {
				t.Fatalf("linked row missing or wrong discovery count: %v %d", found, calls)
			}

			// Last must use a known-open local Session instead of a closed/unknown current-folder link.
			if state != "open" {
				if err := h.last(context.Background(), client); err != nil {
					t.Fatal(err)
				}

				if !strings.Contains(out.String(), local.session) || strings.Contains(out.String(), remote.session) {
					t.Fatal("last did not select the known-open local Session")
				}
			}
		})
	}
}
