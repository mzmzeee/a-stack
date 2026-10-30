package main

import (
	"database/sql"
	"github.com/mzmzeee/a-stack/internal/stack"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestActiveHookOrchestratorReachable(t *testing.T) {
	f := newTeam(t)
	hookHome(t)
	f.receipt(f.orchFile, f.scoped("agent", "attach", "--agent", f.orch, "--runtime-id", "orch-runtime", "--expected-revision", "1", "--operation-id", "race-orch-attach")...)
	f.taskChange(f.orchFile, "race-orch-assign", 2, "assign", "--agent", f.orch)
	id := hookIdentity{harness: "claude", state: f.state, credential: f.orchFile, agent: f.orch, runtimeID: "orch-runtime", worktree: f.project, ref: stack.Context{WorkspaceID: f.workspace, SessionID: f.session}, noStopGuard: true}
	enrollTestHook(t, id, "abc", "")
	hookEvent(t, id, "SessionStart", "")
	hookEvent(t, id, "PostToolUse", "")
	// Model sixteen minutes of continuous work without changing runtime state.
	db, err := sql.Open("sqlite3", filepath.Join(f.state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old := time.Now().Add(-16 * time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err = db.Exec("UPDATE agents SET observed_at=? WHERE id=?", old, f.orch); err != nil {
		t.Fatal(err)
	}
	f.receipt(f.workerFile, f.scoped("mail", "send", "--task", f.task, "--body", "Blocking question @01-orchestrator", "--urgent", "--operation-id", "race-active-hook-mail")...)
	out := hookEvent(t, id, "PostToolUse", "")
	if !strings.Contains(out, "Urgent Task mail") {
		t.Fatalf("fixture hook did not receive mail: %s", out)
	}
	var observed string
	if err = db.QueryRow("SELECT observed_at FROM agents WHERE id=?", f.orch).Scan(&observed); err != nil {
		t.Fatal(err)
	}
	inspected, stderr, code := f.run(f.orchFile, f.scoped("session", "inspect", "--json")...)
	if code != 0 {
		t.Fatalf("inspect: %s", stderr)
	}
	if observed == old {
		t.Fatal("active hook did not refresh observation")
	}
	hookEvent(t, id, "PostToolUse", "")
	var unchanged string
	if err = db.QueryRow("SELECT observed_at FROM agents WHERE id=?", f.orch).Scan(&unchanged); err != nil {
		t.Fatal(err)
	}
	if unchanged != observed {
		t.Fatal("fresh observation should not be rewritten on every tool call")
	}
	if strings.Contains(inspected, "the Orchestrator cannot be reached by mail") {
		t.Fatal("FALSE UNREACHABLE: enrolled active hook just delivered urgent mail, but session inspect tells workers Orchestrator cannot be reached")
	}
}
