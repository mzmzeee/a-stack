package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestMailReplyAndRecipients(t *testing.T) {
	f := newTeam(t)
	original := f.receipt(f.workerFile, f.scoped("mail", "send", "--task", f.task, "--body", "Original", "--operation-id", "reply-original")...)

	args := f.scoped("mail", "reply", "--message", original.ResourceID, "--body", "Answer", "--operation-id", "reply-answer")
	out, stderr, code := f.run(f.workerFile, args...)
	if code != 0 || !strings.Contains(out, "shared with all current assignees") || !strings.Contains(out, "Build the parser") || !strings.Contains(out, f.task) {
		t.Fatalf("reply: %d %s %s", code, out, stderr)
	}

	again, stderr, code := f.run(f.workerFile, args...)
	if code != 0 || again != out {
		t.Fatalf("retry: %d %s %s", code, again, stderr)
	}

	_, stderr, code = f.run(f.workerFile, f.scoped("mail", "reply", "--message", original.ResourceID, "--task", "wrong", "--body", "Answer", "--operation-id", "reply-conflict")...)
	if code == 0 {
		t.Fatalf("conflicting Task accepted: %s", stderr)
	}

	out, stderr, code = f.run(f.workerFile, f.scoped("mail", "recipients", "--task", f.task)...)
	if code != 0 || !strings.Contains(out, "02-worker") {
		t.Fatalf("recipients: %d %s %s", code, out, stderr)
	}

	out, _, code = f.run(f.orchFile, f.scoped("mail", "reply", "--message", original.ResourceID, "--body", "Unauthorized", "--operation-id", "reply-forbidden")...)
	if code == 0 || strings.Contains(out, "Original") {
		t.Fatal("unassigned Orchestrator replied")
	}
}

func TestHandoffMailBudgetAndContinuation(t *testing.T) {
	f := newTeam(t)
	for i := range 4 {
		f.receipt(f.workerFile, f.scoped("mail", "send", "--task", f.task, "--body", strings.Repeat("<", stack.MaxMailBodyBytes), "--operation-id", fmt.Sprintf("budget-mail-%d", i))...)
	}

	out, stderr, code := f.run(f.workerFile, f.scoped("task", "handoff", "--task", f.task, "--limit", "4", "--json")...)
	var result struct {
		Data handoff `json:"data"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &result) != nil {
		t.Fatalf("handoff: %d %s", code, stderr)
	}

	m := result.Data.Mail
	if !m.More || m.Total != 4 || m.First != 1 || m.Last >= 4 || m.Cursor == "" {
		t.Fatalf("budget coverage: %+v", m)
	}

	out, stderr, code = f.run(f.workerFile, f.scoped("mail", "read", "--task", f.task, "--cursor", m.Cursor)...)
	var page struct {
		Data stack.MailPage `json:"data"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &page) != nil || len(page.Data.Messages) == 0 || page.Data.Messages[0].Sequence != m.Last+1 {
		t.Fatalf("continuation failed: %d %s", code, stderr)
	}

	out, stderr, code = f.run(f.orchFile, f.scoped("task", "handoff", "--task", f.task, "--json")...)
	if code != 0 || strings.Contains(out, "first_sequence") || strings.Contains(out, "last_sequence") || strings.Contains(out, "cursor") {
		t.Fatalf("unassigned coverage leaked: %d %s", code, stderr)
	}
}

func TestHandoffMailCoverage(t *testing.T) {
	f := newTeam(t)
	for _, id := range []string{"coverage-first", "coverage-second", "coverage-third"} {
		f.receipt(f.workerFile, f.scoped("mail", "send", "--task", f.task, "--body", id, "--operation-id", id)...)
	}

	out, stderr, code := f.run(f.workerFile, f.scoped("task", "handoff", "--task", f.task, "--limit", "2", "--json")...)
	var result struct {
		Data struct {
			Mail struct {
				First   int64  `json:"first_sequence"`
				Last    int64  `json:"last_sequence"`
				Earlier int64  `json:"earlier_messages"`
				Cursor  string `json:"cursor"`
			} `json:"mail"`
		} `json:"data"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &result) != nil {
		t.Fatalf("handoff: %s %s", out, stderr)
	}

	m := result.Data.Mail
	if m.First != 2 || m.Last != 3 || m.Earlier != 1 || m.Cursor == "" {
		t.Fatalf("missing coverage: %s", out)
	}

	out, _, _ = f.run(f.workerFile, f.scoped("task", "handoff", "--task", f.task, "--limit", "2")...)
	if !strings.Contains(out, "1 earlier messages not shown") {
		t.Fatalf("missing omission: %s", out)
	}
}
