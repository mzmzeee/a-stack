package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// task handoff prints what a fresh Agent needs to take over a Task, and
// refreshes own-mail-read evidence without changing the Task or Session lifecycle.
func TestTaskHandoffBrief(t *testing.T) {
	f := newTeam(t)
	design := f.receipt(f.orchFile, f.scoped("task", "create", "--title", "Design the grammar", "--operation-id", "handoff-design")...).ResourceID
	done := f.receipt(f.orchFile, f.scoped("task", "create", "--title", "Pick a lexer", "--operation-id", "handoff-done")...).ResourceID
	f.receipt(f.orchFile, f.scoped("dependency", "add", "--from", design, "--to", f.task, "--expected-revision", "4", "--operation-id", "handoff-edge-1")...)
	f.receipt(f.orchFile, f.scoped("dependency", "add", "--from", done, "--to", f.task, "--expected-revision", "5", "--operation-id", "handoff-edge-2")...)
	f.receipt(f.orchFile, f.scoped("task", "assign", "--task", done, "--agent", f.worker, "--expected-revision", "1", "--operation-id", "handoff-assign-done")...)
	f.receipt(f.workerFile, f.scoped("task", "status", "--task", done, "--progress", "finished", "--summary", "Hand-written", "--expected-revision", "2", "--operation-id", "handoff-finish-done")...)
	rev := f.taskChange(f.workerFile, "handoff-working", 2, "status", "--progress", "working")
	rev = f.taskChange(f.workerFile, "handoff-report", rev, "report", "--report", "Parser half done.\nNext: errors.")
	f.taskChange(f.workerFile, "handoff-ask", rev, "attention", "set", "--question", "Strict or lenient?")

	for i, body := range []string{"First note", "Second note\nwith two lines", "Third \x1b[31mred"} {
		f.receipt(f.workerFile, f.scoped("mail", "send", "--task", f.task, "--body", body, "--operation-id", "handoff-mail-"+string(rune('a'+i)))...)
	}

	snapshot := func() string {
		out, _, _ := f.run("", f.scoped("session", "snapshot")...)
		var s struct {
			Data struct {
				Session json.RawMessage `json:"session"`
			} `json:"data"`
		}
		_ = json.Unmarshal([]byte(out), &s)
		var stable map[string]any
		_ = json.Unmarshal(s.Data.Session, &stable)
		delete(stable, "read_revision") // own mail reads refresh the read projection
		data, _ := json.Marshal(stable)

		return string(data)
	}
	before := snapshot()

	out, errout, code := f.run(f.workerFile, f.scoped("task", "handoff", "--task", f.task, "--limit", "2")...)
	if code != 0 {
		t.Fatalf("handoff: %d %s", code, errout)
	}

	for _, want := range []string{
		"# Hand-over: Build the parser\n\nTask ID: " + f.task + "\nSession ID: " + f.session + "\nWorkspace ID: " + f.workspace + "\nProgress: working, revision ",
		"## Description\n\n(none)\n",
		"## Report\n\nParser half done.\nNext: errors.\n",
		"## Needs you\n\nOpen question from 02-worker, asked ",
		"not answered yet:\nStrict or lenient?\n",
		"## Unfinished prerequisites\n\n- Design the grammar (" + design + "): waiting, Not started\n\n",
		"## Assignees\n\n- @02-worker (" + f.worker + ")\n",
		"## Task mail, the last 2 of 3\n\n[2] ",
		" 02-worker:\nSecond note\nwith two lines\n\n[3] ",
		"Third \uFFFD[31mred\n",
		"`task show --task " + f.task + "`",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("handoff text has no %q:\n%s", want, out)
		}
	}

	if strings.Contains(out, "Pick a lexer") || strings.Contains(out, "First note") || strings.Contains(out, "\x1b") {
		t.Fatalf("handoff text:\n%s", out)
	}

	out, _, _ = f.run(f.workerFile, f.scoped("task", "handoff", "--task", f.task, "--json")...)
	var brief struct {
		Data handoff `json:"data"`
	}
	if json.Unmarshal([]byte(out), &brief) != nil {
		t.Fatalf("handoff --json: %s", out)
	}

	h := brief.Data
	if h.Task.ID != f.task || h.Task.Question == nil || h.AskerName != "02-worker" || len(h.Prerequisites) != 1 || h.Prerequisites[0].ID != design || len(h.Assignees) != 1 || !h.Mail.Readable || h.Mail.Total != 3 || len(h.Mail.Messages) != 3 || h.Mail.Messages[0].Sequence != 1 {
		t.Fatalf("handoff --json: %+v", h)
	}

	// The Operator reads the Task but not its mail.
	out, errout, code = f.run("", f.scoped("task", "handoff", "--task", f.task)...)
	if code != 0 || !strings.Contains(out, "## Task mail\n\n3 messages. Only assignees read Task mail, and this credential is not one.\n") {
		t.Fatalf("Operator handoff: %d %s %s", code, out, errout)
	}

	// Its next steps do not send it to the mail it cannot read.
	if strings.Contains(out, "check your Task mail") || !strings.Contains(out, "for its current revision. Only assignees read its Task mail: once you are assigned, `mail read --task "+f.task+"`") {
		t.Fatalf("Operator handoff next steps:\n%s", out)
	}

	if _, errout, code = f.run(f.workerFile, f.scoped("task", "handoff", "--task", f.task, "--limit", "0")...); code != 2 || !strings.Contains(errout, "--limit takes 1 to 500 messages") {
		t.Fatalf("handoff --limit 0: %d %s", code, errout)
	}

	if snapshot() != before {
		t.Fatal("task handoff changed the Session")
	}

	if _, errout, code = f.run(f.workerFile, f.scoped("task", "handoff")...); code != 2 || !strings.Contains(errout, "--task") {
		t.Fatalf("handoff without --task: %d %s", code, errout)
	}
}

// The brief shows the Operator's latest answer, also after a new question.
func TestTaskHandoffShowsTheLatestAnswer(t *testing.T) {
	f := newTeam(t)
	f.taskChange(f.workerFile, "handoff-ask", 2, "attention", "set", "--question", "Strict or lenient?")

	out, _, _ := f.run("", f.scoped("task", "show", "--task", f.task, "--json")...)
	var shown struct {
		Data struct {
			Question struct{ ID string } `json:"question"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(out), &shown) != nil || shown.Data.Question.ID == "" {
		t.Fatalf("task show: %s", out)
	}

	f.receipt("", f.scoped("answer", "--task", f.task, "--question-id", shown.Data.Question.ID, "--body", "Lenient,\nwith warnings.", "--operation-id", "handoff-answer")...)

	out, errout, code := f.run(f.workerFile, f.scoped("task", "handoff", "--task", f.task)...)
	if code != 0 || !strings.Contains(out, "## Needs you\n\nNo open question.\nThe Operator answered the latest question, ") || !strings.Contains(out, ":\nStrict or lenient?\nAnswer:\nLenient,\nwith warnings.\n\n## Unfinished") {
		t.Fatalf("handoff after the answer: %d %s\n%s", code, errout, out)
	}
}

// A new Task has no mail, no prerequisites and no assignees.
func TestTaskHandoffOfANewTask(t *testing.T) {
	f := newTeam(t)
	id := f.receipt(f.orchFile, f.scoped("task", "create", "--title", "Fresh", "--description", "Write the docs", "--operation-id", "handoff-fresh")...).ResourceID

	out, errout, code := f.run(f.orchFile, f.scoped("task", "handoff", "--task", id)...)
	if code != 0 {
		t.Fatalf("handoff: %d %s", code, errout)
	}

	for _, want := range []string{
		"Progress: waiting, revision 1", "Reason: Not started\n", "## Description\n\nWrite the docs\n", "## Report\n\n(no report yet)\n", "No open question.\n", "## Unfinished prerequisites\n\nNone.\n", "None: the Task is unassigned.\n",
		// The Orchestrator is not assigned, so it reads no mail.
		"## Task mail\n\n0 messages. Only assignees read Task mail",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("handoff has no %q:\n%s", want, out)
		}
	}

	if help := helpOutput(t, "task", "handoff", "--help"); !strings.Contains(help, "How many of the latest Task mails to show") {
		t.Fatalf("handoff help:\n%s", help)
	}
}
