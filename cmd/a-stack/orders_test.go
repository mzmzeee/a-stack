package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// TestStandingOrdersCLIAndReminder: the Operator sets the standing orders
// from a file; an Agent reads them with workspace orders show and session
// inspect, and its SessionStart reminder shows the first lines and the
// command for the rest.
func TestStandingOrdersCLIAndReminder(t *testing.T) {
	f := newTeam(t)
	hookHome(t)
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, fmt.Sprintf("Rule %d: keep it short.", i))
	}
	file := filepath.Join(t.TempDir(), "orders.txt")
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, errout, code := f.run(f.orchFile, "workspace", "orders", "set", "--workspace", f.workspace, "--body", "x", "--expected-revision", "1", "--operation-id", "orders-agent"); code != 1 || !strings.Contains(errout, "forbidden") {
		t.Fatalf("an Agent set orders: %d %s", code, errout)
	}
	if _, errout, code := f.run("", "workspace", "orders", "set", "--workspace", f.workspace, "--body", "x", "--body-file", file, "--expected-revision", "1", "--operation-id", "orders-both"); code != 2 || !strings.Contains(errout, "choose one of --body and --body-file") {
		t.Fatalf("--body and --body-file: %d %s", code, errout)
	}
	if r := f.receipt("", "workspace", "orders", "set", "--workspace", f.workspace, "--body-file", file, "--expected-revision", "1", "--operation-id", "orders-set"); r.Revision != 2 || r.Outcome != "set" {
		t.Fatalf("set: %+v", r)
	}
	out, errout, code := f.run(f.workerFile, f.scoped("workspace", "orders", "show")...)
	var shown struct {
		Data stack.StandingOrders `json:"data"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &shown) != nil || shown.Data.Revision != 2 || !strings.HasPrefix(shown.Data.Body, "Rule 1:") {
		t.Fatalf("show: %d %s %s", code, out, errout)
	}
	if out, _, _ = f.run(f.workerFile, f.scoped("session", "inspect")...); !strings.Contains(out, `"standing_orders":{"body":"Rule 1: keep it short.\n`) {
		t.Fatalf("session inspect: %s", out)
	}

	if out, errout, code = f.run(f.workerFile, f.scoped("task", "handoff", "--task", f.task)...); code != 0 || !strings.Contains(out, "\n## Standing orders\n\nThe Operator's rules for every Agent of the Workspace. They override your own defaults.\n\nRule 1: keep it short.\n") || !strings.Contains(out, "Rule 10: keep it short.\n\n## Description") {
		t.Fatalf("task handoff: %d %s %s", code, out, errout)
	}

	id := hookIdentity{harness: "claude", state: f.state, credential: f.workerFile, agent: f.worker, worktree: f.project, ref: stack.Context{WorkspaceID: f.workspace, SessionID: f.session}}
	enrollTestHook(t, id, "abc", "")
	text := hookContextText(t, hookEvent(t, id, "SessionStart", `,"source":"compact"`), "SessionStart")
	for _, want := range []string{"Standing orders of this Workspace, from the Operator. They override your own defaults:\n  Rule 1: keep it short.\n", "  Rule 8: keep it short.\n", "  (2 more lines; read them all with: " + agentCommand(id) + " workspace orders show)\n"} {
		if !strings.Contains(text, want) {
			t.Fatalf("reminder lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Rule 9") {
		t.Fatalf("reminder shows more than 8 lines:\n%s", text)
	}

	if r := f.receipt("", "workspace", "orders", "clear", "--workspace", f.workspace, "--expected-revision", "2", "--operation-id", "orders-clear"); r.Revision != 3 || r.Outcome != "cleared" {
		t.Fatalf("clear: %+v", r)
	}
	if text = hookContextText(t, hookEvent(t, id, "SessionStart", ""), "SessionStart"); strings.Contains(text, "Standing orders") {
		t.Fatalf("reminder after clear:\n%s", text)
	}
}

// TestStandingOrdersInTheOrchestratorBrief: the self-setup hand-off names the
// Workspace's standing orders.
func TestStandingOrdersInTheOrchestratorBrief(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	launch(t, dir)
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	handOff := func() (workspace, brief string) {
		var stdout, stderr bytes.Buffer
		var made struct {
			Data struct {
				WorkspaceID string `json:"workspace_id"`
				Brief       string `json:"brief"`
			} `json:"data"`
		}
		if code := run([]string{"new", "--orchestrator", "01-orch", "--state-dir", dir, "--json", "--no-browser"}, &stdout, &stderr); code != 0 || json.Unmarshal(stdout.Bytes(), &made) != nil {
			t.Fatalf("new --orchestrator: exit %d %s", code, stderr.String())
		}
		return made.Data.WorkspaceID, made.Data.Brief
	}
	workspace, brief := handOff()
	if strings.Contains(brief, "Standing orders") {
		t.Fatalf("brief without orders: %s", brief)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"workspace", "orders", "set", "--workspace", workspace, "--body", "Commit as mzmzeee only.", "--expected-revision", "1", "--operation-id", "orders-brief", "--state-dir", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("set: %d %s", code, stderr.String())
	}
	if _, brief = handOff(); !strings.HasSuffix(brief, "\nStanding orders of this Workspace, from the Operator. They override your own defaults:\n  Commit as mzmzeee only.") {
		t.Fatalf("brief: %s", brief)
	}
}
