package stack

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func ackSend(t *testing.T, f mailTeam, from []string, due string) Receipt {
	t.Helper()
	r, err := f.service.Store.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: opaqueID(), Body: "Please confirm @02-worker @03-worker", AckRequired: true, AckFrom: from, AckDue: due})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func ackCheck(t *testing.T, f mailTeam, token string) MailCheckPage {
	t.Helper()
	r, err := f.service.Store.CheckMail(ctx, token, MailCheckRequest{Context: f.ref(0)})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestMailAckExplicitIndependentAndIdempotent(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	r := ackSend(t, f, nil, "1h")
	first := f.read(t, f.b.Token, "", 10)
	if len(first.Messages[0].Acks) != 2 || len(first.PendingAcks) != 2 {
		t.Fatal(first)
	}
	f.read(t, f.b.Token, first.Cursor, 10)
	if len(ackCheck(t, f, f.b.Token).PendingAcks) != 1 {
		t.Fatal("read or cursor acknowledged")
	}
	// Completion is independent of these requests.
	f.change(t, f.a.Token, "status", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 3, Progress: "finished", Summary: "done"})
	if len(ackCheck(t, f, f.b.Token).PendingAcks) != 1 {
		t.Fatal("completion acknowledged")
	}
	request := MailAckRequest{Context: f.ref(0), OperationID: opaqueID(), MessageID: r.ResourceID}
	a, err := s.AckMail(ctx, f.a.Token, request)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.AckMail(ctx, f.a.Token, request)
	if err != nil || a != retry {
		t.Fatal("retry changed receipt", err)
	}
	before := f.read(t, f.a.Token, "", 10).Messages[0].Acks
	request.OperationID = opaqueID()
	if _, err = s.AckMail(ctx, f.a.Token, request); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, f.read(t, f.a.Token, "", 10).Messages[0].Acks) {
		t.Fatal("repeat ack changed actor/time")
	}
	if len(ackCheck(t, f, f.a.Token).PendingAcks) != 0 || len(ackCheck(t, f, f.b.Token).PendingAcks) != 1 {
		t.Fatal("another assignee acknowledged")
	}
	if _, err = s.AckMail(ctx, f.b.Token, MailAckRequest{Context: f.ref(0), OperationID: opaqueID(), MessageID: r.ResourceID}); err != nil {
		t.Fatal(err)
	}
	if countTest(t, s, "SELECT count(*) FROM mail_acks WHERE acked_at<>''") != 2 || countTest(t, s, "SELECT count(*) FROM mail_messages") != 1 {
		t.Fatal("ack generated mail or obligation")
	}
}

func TestMailAckRecipientScopeAndWithdrawal(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	r := ackSend(t, f, []string{"03-worker"}, "")
	req := MailAckRequest{Context: f.ref(0), OperationID: opaqueID(), MessageID: r.ResourceID}
	for _, token := range []string{f.a.Token, f.orch.Token, f.operator.Token} {
		_, err := s.AckMail(ctx, token, req)
		requireCode(t, err, "forbidden")
	}
	foreign := req
	foreign.Context = f.ref(1)
	if _, err := s.AckMail(ctx, f.b.Token, foreign); err == nil {
		t.Fatal("cross Session ack")
	}
	f.change(t, f.orch.Token, "unassign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 3, AgentID: f.bID})
	_, err := s.AckMail(ctx, f.b.Token, req)
	requireCode(t, err, "forbidden")
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 4, AgentID: f.bID})
	_, err = s.AckMail(ctx, f.b.Token, req)
	requireCode(t, err, "forbidden")
	if len(ackCheck(t, f, f.b.Token).PendingAcks) != 0 {
		t.Fatal("rejoining restored obligation")
	}
	if countTest(t, s, "SELECT count(*) FROM mail_acks WHERE withdrawn_at<>'' AND acked_at='' AND reminder_state='suppressed'") != 1 {
		t.Fatal("withdrawal not retained")
	}
}

func TestMailAckValidationAndAtomicity(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	for _, r := range []MailSendRequest{
		{AckDue: "1h"}, {AckFrom: []string{"03-worker"}}, {AckRequired: true},
		{AckRequired: true, AckFrom: []string{"missing"}},
		{AckRequired: true, AckFrom: []string{"03-worker"}, AckDue: "bad"},
		{AckRequired: true, AckFrom: []string{"03-worker"}, AckDue: "0s"},
		{AckRequired: true, AckFrom: []string{"03-worker"}, AckDue: "-1s"},
	} {
		r.Context = f.ref(0)
		r.TaskID = f.taskID
		r.OperationID = opaqueID()
		r.Body = "request"
		_, err := s.SendMail(ctx, f.a.Token, r)
		requireCode(t, err, "invalid_input")
	}
	if countTest(t, s, "SELECT count(*) FROM mail_messages") != 0 {
		t.Fatal("invalid send persisted mail")
	}
	execTest(t, s, "CREATE TRIGGER reject_ack BEFORE INSERT ON mail_acks BEGIN SELECT RAISE(ABORT,'injected'); END")
	_, err := s.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: opaqueID(), Body: "@03-worker", AckRequired: true})
	if err == nil || countTest(t, s, "SELECT count(*) FROM mail_messages") != 0 || countTest(t, s, "SELECT count(*) FROM mail_mentions") != 0 {
		t.Fatal("partial send", err)
	}
	execTest(t, s, "DROP TRIGGER reject_ack")
	r := ackSend(t, f, []string{"03-worker", "03-worker"}, "1m")
	execTest(t, s, "CREATE TRIGGER reject_ack_update BEFORE UPDATE ON mail_acks BEGIN SELECT RAISE(ABORT,'injected'); END")
	_, err = s.AckMail(ctx, f.b.Token, MailAckRequest{Context: f.ref(0), OperationID: opaqueID(), MessageID: r.ResourceID})
	if err == nil || countTest(t, s, "SELECT count(*) FROM mail_acks WHERE acked_at<>''") != 0 {
		t.Fatal("partial ack")
	}
	_, err = s.ChangeTask(ctx, f.orch.Token, "unassign", TaskChangeRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: opaqueID(), ExpectedRevision: 3, AgentID: f.bID})
	if err == nil || countTest(t, s, "SELECT count(*) FROM task_assignees") != 2 {
		t.Fatal("partial unassignment")
	}
}

func TestMailAckReplyAndClosedSession(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	parent := f.send(t, f.a.Token, "parent")
	r, err := s.ReplyMail(ctx, f.b.Token, MailReplyRequest{MailSendRequest: MailSendRequest{Context: f.ref(0), OperationID: opaqueID(), Body: "confirm", AckRequired: true}, MessageID: parent.ResourceID})
	if err != nil {
		t.Fatal(err)
	}
	if a := ackCheck(t, f, f.a.Token).PendingAcks; len(a) != 1 || a[0].MessageID != r.ResourceID {
		t.Fatal(a)
	}
	req := MailAckRequest{Context: f.ref(0), OperationID: opaqueID(), MessageID: r.ResourceID}
	receipt, err := s.AckMail(ctx, f.a.Token, req)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ChangeSession(ctx, f.operator.Token, "close", SessionChangeRequest{Context: f.ref(0), OperationID: opaqueID(), ExpectedRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.AckMail(ctx, f.a.Token, req)
	if err != nil || again != receipt {
		t.Fatal("closed retry", err)
	}
	req.OperationID = opaqueID()
	if _, err = s.AckMail(ctx, f.a.Token, req); err == nil {
		t.Fatal("new ack in closed Session")
	}
}

func TestMailAckOverdueReminderBounded(t *testing.T) {
	f := noticeTestFixture(t)
	s := f.service.Store
	ackSend(t, f.mailTeam, []string{"03-worker"}, "1h")
	if r := f.drain(t, 10); r.Submitted != 1 {
		t.Fatal(r)
	}
	if len(ackCheck(t, f.mailTeam, f.b.Token).PendingAcks) != 1 {
		t.Fatal("submission acknowledged")
	}
	f.n.clock = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if r := f.drain(t, 10); r.Submitted != 1 {
		t.Fatal(r)
	}
	for range 3 {
		if r := f.drain(t, 10); r.Checked != 0 {
			t.Fatal("repeated overdue reminder", r)
		}
	}
	if f.prompts(t) != 2 || countTest(t, s, "SELECT count(*) FROM notice_ack_refs") != 1 || countTest(t, s, "SELECT count(*) FROM mail_messages") != 1 {
		t.Fatal("unbounded reminder or ack-of-ack")
	}
	if countTest(t, s, "SELECT count(*) FROM mail_acks WHERE acked_at=''") != 1 {
		t.Fatal("reminder acknowledged")
	}
}

func TestMailAckReminderRecoveryAndSuppression(t *testing.T) {
	for _, mode := range []string{"recover", "ack", "unassign"} {
		t.Run(mode, func(t *testing.T) {
			f := noticeTestFixture(t)
			s := f.service.Store
			r := ackSend(t, f.mailTeam, []string{"03-worker"}, "1h")
			f.drain(t, 10)
			f.n.clock = func() time.Time { return time.Now().Add(2 * time.Hour) }
			target, err := f.n.nextTarget(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			batch, err := f.n.claim(ctx, target)
			if err != nil || batch.id == "" {
				t.Fatal(batch, err)
			}
			switch mode {
			case "recover":
				if err = s.recoverNotices(ctx); err != nil {
					t.Fatal(err)
				}
			case "ack":
				_, err = s.AckMail(ctx, f.b.Token, MailAckRequest{Context: f.ref(0), OperationID: opaqueID(), MessageID: r.ResourceID})
			case "unassign":
				f.change(t, f.orch.Token, "unassign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 3, AgentID: f.bID})
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode != "recover" {
				state, _, err := f.n.preflight(ctx, batch)
				if err != nil || state != NoticeSuppressed {
					t.Fatal(state, err)
				}
				if err = f.n.finish(batch, NoticeSubmitted, "submitted"); err != nil {
					t.Fatal(err)
				}
			}
			if r := f.drain(t, 10); r.Checked != 0 {
				t.Fatal(r)
			}
			if f.prompts(t) != 1 {
				t.Fatal("stale reminder submitted")
			}
		})
	}
}

func TestMailAckNowCount(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	r := ackSend(t, f, []string{"03-worker"}, "1h")
	execTest(t, s, fmt.Sprintf("UPDATE mail_acks SET due_at='2000-01-01T00:00:00Z' WHERE message_id='%s'", r.ResourceID))
	task, err := s.ShowTask(ctx, f.a.Token, TaskReadRequest{Context: f.ref(0), TaskID: f.taskID})
	if err != nil || task.OverdueAcks != 1 || task.NeedsYou {
		t.Fatal(task, err)
	}
}
