package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// legacyCopy writes a credential file as a-stack wrote it before v0.1.5:
// kind and token, without Workspace, Session and Agent name.
func legacyCopy(t *testing.T, file string) string {
	t.Helper()
	c, err := stack.ReadCredential(file)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"kind": c.Kind, "token": c.Token})
	legacy := filepath.Join(filepath.Dir(file), "legacy.json")
	if err = os.WriteFile(legacy, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return legacy
}

// A credential file without Workspace and Session is enough: the CLI asks
// the service for the token's own context and leaves the file as it is.
func TestCLILegacyCredentialFileGetsItsContextFromTheService(t *testing.T) {
	f := newTeam(t)
	legacy := legacyCopy(t, f.workerFile)
	before, _ := os.ReadFile(legacy)
	out, errout, code := f.run(legacy, "session", "inspect")
	if code != 0 || !strings.Contains(out, `"your_agent_id":"`+f.worker+`"`) || !strings.Contains(out, `"id":"`+f.session+`"`) {
		t.Fatalf("session inspect: exit %d %s %s", code, out, errout)
	}
	if out, errout, code = f.run(legacy, "mail", "check"); code != 0 || !strings.Contains(out, f.task) {
		t.Fatalf("mail check: exit %d %s %s", code, out, errout)
	}
	f.receipt(legacy, "task", "status", "--task", f.task, "--progress", "working", "--expected-revision", "2", "--operation-id", "legacy-start")
	// A flag that names another Session is refused before the request.
	if _, errout, code = f.run(legacy, "task", "list", "--session", f.workspace); code != 2 || !strings.Contains(errout, "--session does not match the Session of the credential file") {
		t.Fatalf("other Session: exit %d %s", code, errout)
	}
	if after, _ := os.ReadFile(legacy); !bytes.Equal(before, after) {
		t.Fatal("the CLI rewrote the credential file")
	}
}

// A service before v0.1.7 cannot name the context; the error then says which
// flags to add and where to find the IDs.
func TestLegacyCredentialFileWithAnOldService(t *testing.T) {
	calls := 0
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"endpoint not found","retryable":false}}`))
	}))
	defer old.Close()
	client := &stack.Client{Discovery: stack.Discovery{URL: old.URL, InstanceID: "old"}, Credential: stack.Credential{Kind: stack.AgentCapability, Token: strings.Repeat("A", 43)}, HTTP: old.Client()}
	err := loadContext(context.Background(), client, lifecycleFlags{}, "task list")
	if err != legacyContext || !strings.Contains(err.Error(), "add --workspace WORKSPACE_ID --session SESSION_ID") || calls != 1 {
		t.Fatalf("old service: %v after %d calls", err, calls)
	}
	// With both flags, or a command without a Session, nothing is asked.
	id := strings.Repeat("B", 43)
	if loadContext(context.Background(), client, lifecycleFlags{workspace: id, session: id}, "task list") != nil || loadContext(context.Background(), client, lifecycleFlags{}, "health") != nil || calls != 1 {
		t.Fatalf("asked the service without need: %d calls", calls)
	}
}

// hook install takes the Workspace, Session and Agent of a credential file
// from before v0.1.5 from the service too, and the hook command names them.
func TestHookInstallLegacyCredentialFileGetsItsContextFromTheService(t *testing.T) {
	f := newTeam(t)
	home := hookHome(t)
	legacy := legacyCopy(t, f.workerFile)
	t.Chdir(f.project)
	out, errout, code := f.run("", "hook", "install", "--claude", "--credential-file", legacy)
	if code != 0 {
		t.Fatalf("install: exit %d %s %s", code, out, errout)
	}
	c := commands(readSettings(t, filepath.Join(home, ".claude", "settings.json")), "Stop")
	if len(c) != 1 || !strings.Contains(c[0], " --workspace "+f.workspace+" --session "+f.session+" --agent "+f.worker+" ") {
		t.Fatalf("hook command: %v", c)
	}
}
