package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestHookUrgentIdentityChange(t *testing.T) {
	f := newTeam(t)
	home := hookHome(t)
	f.taskChange(f.orchFile, "transfer-hook-assign", 2, "assign", "--agent", f.orch)
	id := hookIdentity{ref: stack.Context{WorkspaceID: f.workspace, SessionID: f.session}, agent: f.worker}

	client, err := stack.NewClient(f.state, f.workerFile, stack.AgentCapability)
	if err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(home, "urgent.json")
	stale := urgentHookCursor{Identity: "old-database-and-credential", Positions: map[string]stack.UrgentMailPosition{f.task: {Sequence: 99999}}, AfterTask: "old-scan"}
	if err = stack.ReplacePrivate(file, stale); err != nil {
		t.Fatal(err)
	}

	send := func(op string, extra ...string) {
		t.Helper()
		args := f.scoped("mail", "send", "--task", f.task, "--body", "private body", "--operation-id", op)
		f.receipt(f.orchFile, append(args, extra...)...)
	}
	send("transfer-old-urgent", "--urgent")

	if got := hookUrgentNudge(context.Background(), client, id, file); got != "" {
		t.Fatal("identity reset replayed history", got)
	}

	data, err := os.ReadFile(file)
	var reset urgentHookCursor
	if err != nil || json.Unmarshal(data, &reset) != nil || reset.Identity == stale.Identity || reset.AfterTask != "" || reset.Positions[f.task].Sequence == 99999 {
		t.Fatal("identity change did not reset cursor", err)
	}

	send("transfer-new-urgent", "--urgent")

	if got := hookUrgentNudge(context.Background(), client, id, file); got != urgentMailNudge {
		t.Fatal("urgent silent after transfer", got)
	}

	if got := hookUrgentNudge(context.Background(), client, id, file); got != "" {
		t.Fatal("urgent replayed", got)
	}

	send("transfer-new-ack", "--ack-required", "--ack-from", "02-worker", "--ack-due", "1ns")

	if got := hookUrgentNudge(context.Background(), client, id, file); got != urgentMailNudge {
		t.Fatal("ACK silent after transfer", got)
	}

	in := hookInput{SessionID: "normal-transfer", Event: "PostToolUse"}

	normal, ok := hookCacheFile(f.worker, in.SessionID, ".json")
	if !ok {
		t.Fatal("normal cache path")
	}

	if err := stack.ReplacePrivate(normal, map[string]string{"identity": "old-identity"}); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-mailNudgeEvery - time.Second)
	if err := os.Chtimes(normal, old, old); err != nil {
		t.Fatal(err)
	}

	hookMailNudge(context.Background(), client, id, in)
	hookMailNudge(context.Background(), client, id, in)
	send("transfer-new-normal")

	if err := os.Chtimes(normal, old, old); err != nil {
		t.Fatal(err)
	}

	if got := hookMailNudge(context.Background(), client, id, in); !strings.Contains(got, "New Task mail") {
		t.Fatal("normal cursor stayed silent after identity change")
	}
}

func TestHookUrgentIdentityCatchupPages(t *testing.T) {
	file := filepath.Join(hookHome(t), "urgent.json")
	if err := stack.ReplacePrivate(file, urgentHookCursor{Identity: "old", Positions: map[string]stack.UrgentMailPosition{}}); err != nil {
		t.Fatal(err)
	}

	page := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request stack.UrgentMailRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}

		if page == 1 && request.AfterTask != "next" {
			t.Error("catch-up did not resume its next page")
		}

		next := ""
		if page == 0 {
			next = "next"
		}

		page++
		_ = json.NewEncoder(w).Encode(map[string]any{"data": stack.UrgentMailPage{Urgent: true, NextTask: next, Positions: map[string]stack.UrgentMailPosition{}}})
	}))
	defer server.Close()

	client := &stack.Client{Discovery: stack.Discovery{URL: server.URL, DatabaseID: "new"}, HTTP: server.Client()}
	for i, want := range []string{"", "", urgentMailNudge} {
		if got := hookUrgentNudge(context.Background(), client, hookIdentity{}, file); got != want {
			t.Fatalf("page %d: got %q, want %q", i, got, want)
		}
	}
}
