package stack

import (
	"testing"
	"time"
)

// A Working Task turns quiet QuietAfter after its last change or Task mail.
// Task show, the task list page and the view's snapshot ETag agree, and no
// revision changes when it turns quiet.
func TestQuietWorkingTasks(t *testing.T) {
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	at := func(d time.Duration) {
		moment := start.Add(d)
		fixedClock.Store(&moment)
	}
	t.Cleanup(func() { fixedClock.Store(nil) })
	at(0)
	f := tasksFixture(t)
	working := f.create(t, "Working").ResourceID
	waiting := f.create(t, "Waiting").ResourceID
	f.assignA(t, working)
	f.assignA(t, waiting)
	f.progress(t, working, "working")
	_, token := viewTest(t, f, f.ref(0))
	poll := viewPoller(t, f, token)
	_, tag, first := poll(0, "")
	revision := first.Session.ReadRevision

	at(QuietAfter - time.Second)

	if f.show(t, working).Quiet {
		t.Fatal("quiet too early")
	}

	if status, _, _ := poll(revision, tag); status != 304 {
		t.Fatal("snapshot changed before the Task turned quiet", status)
	}

	at(QuietAfter)

	if !f.show(t, working).Quiet || f.show(t, waiting).Quiet {
		t.Fatal("quiet is for Working Tasks after QuietAfter")
	}

	page, err := f.service.Store.SnapshotPage(ctx, token, SnapshotPageRequest{Context: f.ref(0), Kind: "tasks"})
	if err != nil {
		t.Fatal(err)
	}

	for _, item := range page.Items {
		if task := item.(Task); task.Quiet != (task.ID == working) {
			t.Fatal("the view's Task page disagrees", task.Title, task.Quiet)
		}
	}

	status, newTag, answer := poll(revision, tag)
	if status != 200 || newTag == tag || answer.Session.ReadRevision != revision {
		t.Fatalf("turning quiet did not reach the view: %d %q", status, newTag)
	}

	if status, _, _ = poll(revision, newTag); status != 304 {
		t.Fatal("the quiet ETag is not stable", status)
	}

	// Task mail restarts the clock, and the Task read says when it came.
	if _, err = f.service.Store.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: working, OperationID: opaqueID(), Body: "Still on it"}); err != nil {
		t.Fatal(err)
	}

	task := f.show(t, working)
	if task.Quiet || task.LastMailAt != start.Add(QuietAfter).Format(time.RFC3339Nano) {
		t.Fatal("mail must end quiet", task.Quiet, task.LastMailAt)
	}

	at(2*QuietAfter - time.Second)

	if f.show(t, working).Quiet {
		t.Fatal("quiet before QuietAfter since the mail")
	}

	at(2 * QuietAfter)

	if !f.show(t, working).Quiet {
		t.Fatal("not quiet QuietAfter after the mail")
	}

	// A question for the Operator is not the Agent going quiet.
	f.change(t, f.a.Token, "attention.set", TaskChangeRequest{TaskID: working, ExpectedRevision: f.show(t, working).Revision, Question: "Which one?"})
	at(4 * QuietAfter)

	if task = f.show(t, working); task.Quiet || !task.NeedsYou {
		t.Fatal("a Task that needs you is not quiet")
	}
}
