package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestWatchStalledMentionOnceAndClear(t *testing.T) {
	var out bytes.Buffer
	w := &watcher{stdout: &out, now: time.Now}

	s := scopedSession{Session: stack.Session{ID: "session", Name: "work"}}
	task := stack.Task{ID: "task", Title: "Question", Progress: "waiting", MailWaits: []stack.MailWait{{RecipientID: "recipient", SenderName: "worker", Since: "2026-01-01T00:00:00Z", Stalled: true, Text: "worker has waited 34 min for orchestrator"}}}
	if err := w.report(s, map[string]stack.Task{task.ID: {ID: task.ID, Progress: "waiting"}}, []stack.Task{task}); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "Stalled: worker has waited 34 min for orchestrator") || !strings.Contains(out.String(), "\a") {
		t.Fatal(out.String())
	}

	out.Reset()

	if err := w.report(s, map[string]stack.Task{task.ID: task}, []stack.Task{task}); err != nil {
		t.Fatal(err)
	}

	if out.Len() != 0 {
		t.Fatal("repeated stall", out.String())
	}

	if got := countTasks(s, []stack.Task{task}); got.Stalled != 1 || !strings.Contains(statusCounts(got), "Stalled") {
		t.Fatal(got)
	}

	task.MailWaits = nil
	if countTasks(s, []stack.Task{task}).Stalled != 0 {
		t.Fatal("cleared stall remains")
	}
}
