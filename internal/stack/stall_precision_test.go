package stack

import (
	"testing"
	"time"
)

func TestUnreadMentionKeepsNanosecondReadOrder(t *testing.T) {
	f := mailFixture(t)
	at := time.Now().UTC().Truncate(time.Second).Add(time.Second)
	fixedClock.Store(&at)
	t.Cleanup(func() { fixedClock.Store(nil) })
	if _, err := f.service.Store.CheckMail(ctx, f.b.Token, MailCheckRequest{Context: f.ref(0)}); err != nil {
		t.Fatal(err)
	}
	later := at.Add(time.Nanosecond)
	fixedClock.Store(&later)
	f.send(t, f.a.Token, "After read @03-worker")
	detail, err := f.service.Store.ShowTask(ctx, f.a.Token, TaskReadRequest{Context: f.ref(0), TaskID: f.taskID})
	if err != nil || len(detail.MailWaits) != 1 {
		t.Fatal("later mention lost to timestamp rounding", detail, err)
	}
	later = at.Add(2 * time.Nanosecond)
	fixedClock.Store(&later)
	if _, err = f.service.Store.CheckMail(ctx, f.b.Token, MailCheckRequest{Context: f.ref(0)}); err != nil {
		t.Fatal(err)
	}
	detail, err = f.service.Store.ShowTask(ctx, f.a.Token, TaskReadRequest{Context: f.ref(0), TaskID: f.taskID})
	if err != nil || len(detail.MailWaits) != 0 {
		t.Fatal("read did not clear earlier mention", detail, err)
	}
}

func TestUnreadMentionDoesNotReviveAfterRejoin(t *testing.T) {
	f := mailFixture(t)
	f.send(t, f.a.Token, "Question @03-worker")
	f.change(t, f.orch.Token, "unassign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 3, AgentID: f.bID})
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 4, AgentID: f.bID})
	detail, err := f.service.Store.ShowTask(ctx, f.a.Token, TaskReadRequest{Context: f.ref(0), TaskID: f.taskID})
	if err != nil || len(detail.MailWaits) != 0 {
		t.Fatal("rejoin revived old wait", detail, err)
	}
}
