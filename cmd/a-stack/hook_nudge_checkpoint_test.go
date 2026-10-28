package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

type nudgeFailShowTransport struct{ base http.RoundTripper }

func (tr nudgeFailShowTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/v1/task/show" {
		return nil, errors.New("title lookup unavailable")
	}
	return tr.base.RoundTrip(r)
}

func TestHookNudgeCheckpointWaitsForText(t *testing.T) {
	f := newTeam(t)
	hookHome(t)
	peerFile := filepath.Join(filepath.Dir(f.workerFile), "peer.json")
	peer := f.receipt(f.orchFile, f.scoped("agent", "register", "--name", "03-peer", "--new-credential-file", peerFile, "--operation-id", "nudge-check-peer")...).ResourceID
	f.taskChange(f.orchFile, "nudge-check-assign", 2, "assign", "--agent", peer)
	id := hookIdentity{state: f.state, credential: f.workerFile, agent: f.worker, worktree: f.project, ref: stack.Context{WorkspaceID: f.workspace, SessionID: f.session}}
	client, err := stack.NewClient(f.state, f.workerFile, stack.AgentCapability)
	if err != nil {
		t.Fatal(err)
	}
	in := hookInput{SessionID: "checkpoint-session", Event: "PostToolUse"}
	if text := hookMailNudge(context.Background(), client, id, in); text != "" {
		t.Fatal("first pass nudged")
	}
	file, ok := hookCacheFile(f.worker, in.SessionID, ".json")
	if !ok {
		t.Fatal("cache path")
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	f.receipt(peerFile, f.scoped("mail", "send", "--task", f.task, "--body", "new mail", "--operation-id", "nudge-check-send")...)
	later := func() {
		old := time.Now().Add(-mailNudgeEvery - time.Second)
		if err := os.Chtimes(file, old, old); err != nil {
			t.Fatal(err)
		}
	}
	later()
	original := client.HTTP.Transport
	base := original
	if base == nil {
		base = http.DefaultTransport
	}
	client.HTTP.Transport = nudgeFailShowTransport{base}
	if text := hookMailNudge(context.Background(), client, id, in); text != "" {
		t.Fatal("failed lookup produced nudge")
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("title failure consumed nudge", err)
	}
	client.HTTP.Transport = original
	later()
	if text := hookMailNudge(context.Background(), client, id, in); !strings.Contains(text, "New Task mail") {
		t.Fatal("retry lost nudge", text)
	}
}
