package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestShareAgentsRejectsUntrustedDiscovery(t *testing.T) {
	for _, name := range []string{"02-worker", "../credentials/worker", `..\credentials\worker`, "bad/name", "bad\\name", ".", "", strings.Repeat("x", 65), "invalid-id"} {
		t.Run(name, func(t *testing.T) {
			f := newTeam(t)
			hookHome(t)

			local, err := stack.NewClient(f.state, "", stack.OperatorCapability)
			if err != nil {
				t.Fatal(err)
			}

			credential, err := stack.ReadCredential(f.workerFile)
			if err != nil {
				t.Fatal(err)
			}

			standard := filepath.Join(filepath.Dir(f.state), "a-stack-agents")
			if err = stack.MkdirPrivate(standard); err != nil {
				t.Fatal(err)
			}

			data, _ := json.Marshal(credential)
			if err = os.WriteFile(filepath.Join(standard, "02-worker.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}

			id := f.worker
			if name == "invalid-id" {
				id = "../worker"
			}

			var probes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/agent/list":
					_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"items": []stack.Agent{{ID: id, Name: name}}}})
				case "/v1/session/inspect":
					probes.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]any{"data": stack.Session{YourAgentID: f.worker}})
				default:
					t.Error("unexpected request", r.URL.Path)
				}
			}))
			defer server.Close()
			// Even a source claiming the local database ID must not receive its token.
			source := *local
			source.Discovery.URL = server.URL
			source.HTTP = server.Client()
			bundle := stack.TransferBundle{TransferID: shareToken(), Session: stack.Session{ID: f.session, WorkspaceID: f.workspace}}
			_, err = prepareShareAgents(context.Background(), f.state, &source, local, bundle, "")

			if probes.Load() != 0 {
				t.Fatal("unrelated Agent credential sent to remote")
			}

			if err == nil {
				t.Fatal("untrusted Agent discovery accepted")
			}

			if name != "02-worker" && !strings.Contains(err.Error(), "invalid Agent ID or name") {
				t.Fatal("hostile name was not rejected before credential discovery", err)
			}
		})
	}
}

func TestShareAgentsCredentialScope(t *testing.T) {
	for _, scenario := range []string{"explicit-first", "missing-enrollment", "other-database", "other-remote"} {
		t.Run(scenario, func(t *testing.T) {
			f := newTeam(t)
			hookHome(t)

			client, err := stack.NewClient(f.state, "", stack.OperatorCapability)
			if err != nil {
				t.Fatal(err)
			}

			credential, err := stack.ReadCredential(f.workerFile)
			if err != nil {
				t.Fatal(err)
			}

			switch scenario {
			case "missing-enrollment":
				credential.Enrollment = nil
			case "other-database":
				credential.Enrollment.DatabaseID = f.workspace
			case "other-remote":
				credential.Remote = "other"
			}

			data, _ := json.Marshal(credential)
			if err = os.WriteFile(f.workerFile, data, 0o600); err != nil {
				t.Fatal(err)
			}

			if scenario == "explicit-first" {
				standard := filepath.Join(filepath.Dir(f.state), "a-stack-agents")
				if err = stack.MkdirPrivate(standard); err != nil {
					t.Fatal(err)
				}

				if err = os.WriteFile(filepath.Join(standard, "02-worker.json"), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			manifest := filepath.Join(t.TempDir(), "agents.json")
			data, _ = json.Marshal(map[string]string{f.orch: f.orchFile, f.worker: f.workerFile})
			if err = os.WriteFile(manifest, data, 0o600); err != nil {
				t.Fatal(err)
			}

			bundle := stack.TransferBundle{TransferID: shareToken(), Session: stack.Session{ID: f.session, WorkspaceID: f.workspace}}
			agents, err := prepareShareAgents(context.Background(), f.state, client, client, bundle, manifest)
			if scenario == "explicit-first" {
				if err != nil || agents[f.worker].Old != f.workerFile {
					t.Fatal("explicit credential was not preferred", err)
				}
			} else if err == nil {
				t.Fatal("credential with mismatched service accepted")
			}
		})
	}
}
