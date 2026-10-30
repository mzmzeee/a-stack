package stack

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestUrgentPriorityRetryAndValidation(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	r := MailSendRequest{Context: f.ref(0), TaskID: f.taskID, Body: "Urgent word alone means nothing", OperationID: opaqueID()}
	normal, err := s.SendMail(ctx, f.a.Token, r)
	if err != nil {
		t.Fatal(err)
	}
	if f.read(t, f.b.Token, "", 100).Messages[0].Priority != "normal" {
		t.Fatal("inferred priority")
	}
	r.Priority = "urgent"
	_, err = s.SendMail(ctx, f.a.Token, r)
	requireCode(t, err, "operation_conflict")
	r.OperationID = opaqueID()
	urgent, err := s.SendMail(ctx, f.a.Token, r)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.SendMail(ctx, f.a.Token, r)
	if err != nil || retry != urgent {
		t.Fatal("retry", retry, err)
	}
	r.OperationID = opaqueID()
	r.Priority = "critical"
	_, err = s.SendMail(ctx, f.a.Token, r)
	requireCode(t, err, "invalid_input")
	r.Priority = "urgent"
	_, err = s.SendMail(ctx, f.orch.Token, r)
	requireCode(t, err, "forbidden")
	reply := MailReplyRequest{MailSendRequest: MailSendRequest{Context: f.ref(0), Body: "stop", Priority: "urgent", OperationID: opaqueID()}, MessageID: normal.ResourceID}
	_, err = s.ReplyMail(ctx, f.b.Token, reply)
	if err != nil {
		t.Fatal(err)
	}
	reply.Priority = "normal"
	_, err = s.ReplyMail(ctx, f.b.Token, reply)
	requireCode(t, err, "operation_conflict")
}

func TestUrgentPeekIndependentAndAuthorized(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	request := UrgentMailRequest{Context: f.ref(0)}
	peek := func() UrgentMailPage {
		t.Helper()
		out, err := s.PeekUrgentMail(ctx, f.b.Token, request)
		if err != nil {
			t.Fatal(err)
		}
		request.Positions = out.Positions
		return out
	}
	if peek().Urgent {
		t.Fatal("empty peek")
	}
	f.send(t, f.a.Token, "normal")
	if peek().Urgent {
		t.Fatal("normal nudged urgently")
	}
	for range 20 {
		if _, err := s.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: f.taskID, Body: "private", Priority: "urgent", OperationID: opaqueID()}); err != nil {
			t.Fatal(err)
		}
	}
	if !peek().Urgent || peek().Urgent {
		t.Fatal("batch missing or repeated")
	}
	if _, err := s.SendMail(ctx, f.b.Token, MailSendRequest{Context: f.ref(0), TaskID: f.taskID, Body: "self", Priority: "urgent", OperationID: opaqueID()}); err != nil {
		t.Fatal(err)
	}
	if peek().Urgent {
		t.Fatal("self nudged")
	}
	for _, token := range []string{f.operator.Token, f.orch.Token} {
		out, err := s.PeekUrgentMail(ctx, token, UrgentMailRequest{Context: f.ref(0)})
		if token == f.operator.Token {
			requireCode(t, err, "forbidden")
		} else if err != nil || out.Urgent || len(out.Positions) != 0 {
			t.Fatal("unassigned leak", out, err)
		}
	}
	f.change(t, f.orch.Token, "unassign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 3, AgentID: f.bID})
	if peek().Urgent {
		t.Fatal("removed recipient")
	}
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 4, AgentID: f.bID})
	request.Positions = nil
	if peek().Urgent {
		t.Fatal("rejoin revived old urgency")
	}
	if countTest(t, s, "SELECT count(*) FROM mail_acks") != 0 {
		t.Fatal("peek created ack")
	}
}

func TestUrgentOverdueAckAndPeekDoesNotRearm(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	r, err := s.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: f.taskID, Body: "confirm", AckRequired: true, AckFrom: []string{"03-worker"}, AckDue: "1h", OperationID: opaqueID()})
	if err != nil {
		t.Fatal(err)
	}
	execTest(t, s, "UPDATE mail_acks SET due_at=? WHERE message_id=?", time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), r.ResourceID)
	out, err := s.PeekUrgentMail(ctx, f.b.Token, UrgentMailRequest{Context: f.ref(0)})
	if err != nil || !out.Urgent {
		t.Fatal("overdue not urgent", out, err)
	}
	again, err := s.PeekUrgentMail(ctx, f.b.Token, UrgentMailRequest{Context: f.ref(0), Positions: out.Positions})
	if err != nil || again.Urgent {
		t.Fatal("overdue replay", again, err)
	}
	if countTest(t, s, "SELECT count(*) FROM mail_acks WHERE acked_at='' AND reminder_state='pending'") != 1 {
		t.Fatal("peek consumed reminder")
	}
	if f.read(t, f.b.Token, "", 100).Messages[0].Priority != "normal" {
		t.Fatal("reminder rewrote message priority")
	}
}

func TestHookPeeksNeverRearmTerminalNotice(t *testing.T) {
	f := noticeTestFixture(t)
	f.send(t, f.a.Token, "first @03-worker")
	f.drain(t, 4)
	f.send(t, f.a.Token, "held @03-worker")
	if _, err := f.service.Store.CheckMail(ctx, f.b.Token, MailCheckRequest{Context: f.ref(0), Peek: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Store.PeekUrgentMail(ctx, f.b.Token, UrgentMailRequest{Context: f.ref(0)}); err != nil {
		t.Fatal(err)
	}
	if f.drain(t, 4).Submitted != 0 || f.prompts(t) != 1 {
		t.Fatal("hook peek re-armed hold")
	}
	if _, err := f.service.Store.CheckMail(ctx, f.b.Token, MailCheckRequest{Context: f.ref(0)}); err != nil {
		t.Fatal(err)
	}
	if f.drain(t, 4).Submitted != 0 {
		t.Fatal("own read bypassed pane cooldown")
	}
	f.n.clock = func() time.Time { return time.Now().Add(noticeCooldown) }
	if f.drain(t, 4).Submitted != 1 {
		t.Fatal("cooldown did not release next batch")
	}
}

func TestUrgentPeekBoundedPagesAndRestart(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	for range 101 {
		task := f.create(t, "PRIVATE TITLE").ResourceID
		f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: task, AgentID: f.aID, ExpectedRevision: 1})
		f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: task, AgentID: f.bID, ExpectedRevision: 2})
		if _, err := s.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: task, Body: "PRIVATE BODY", Priority: "urgent", OperationID: opaqueID()}); err != nil {
			t.Fatal(err)
		}
	}
	r := UrgentMailRequest{Context: f.ref(0), Positions: map[string]UrgentMailPosition{}}
	first, err := s.PeekUrgentMail(ctx, f.b.Token, r)
	if err != nil || len(first.Positions) != 100 || first.NextTask == "" || !first.Urgent {
		t.Fatal("first page", first, err)
	}
	b, _ := json.Marshal(first)
	if len(b) > 20000 || strings.Contains(string(b), "PRIVATE") {
		t.Fatal("unbounded or content-bearing peek")
	}
	for k, v := range first.Positions {
		r.Positions[k] = v
	}
	r.AfterTask = first.NextTask
	_, err = s.PeekUrgentMail(ctx, f.a.Token, r)
	requireCode(t, err, "invalid_input")
	second, err := s.PeekUrgentMail(ctx, f.b.Token, r)
	if err != nil || len(second.Positions) != 2 || second.NextTask != "" || !second.Urgent {
		t.Fatal("second page", second, err)
	}
	for k, v := range second.Positions {
		r.Positions[k] = v
	}
	r.AfterTask = ""
	path := s.path
	if err = f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	again, err := s.PeekUrgentMail(ctx, f.b.Token, r)
	if err != nil || again.Urgent {
		t.Fatal("restart replayed cursor", again, err)
	}
	execTest(t, s, "UPDATE sessions SET closed=1 WHERE id=?", f.ref(0).SessionID)
	out, err := s.PeekUrgentMail(ctx, f.b.Token, UrgentMailRequest{Context: f.ref(0)})
	if err != nil || out.Urgent || len(out.Positions) != 0 {
		t.Fatal("closed session leaked wake", out, err)
	}
}

func TestUrgentPriorityRetained(t *testing.T) {
	f := mailFixture(t)
	r := MailSendRequest{Context: f.ref(0), TaskID: f.taskID, Body: "Stop", OperationID: "urgent-retained-1"}
	b, _ := json.Marshal(r)
	var fields map[string]any
	_ = json.Unmarshal(b, &fields)
	fields["priority"] = "urgent"
	b, _ = json.Marshal(fields)
	_ = json.Unmarshal(b, &r)
	if _, err := f.service.Store.SendMail(ctx, f.a.Token, r); err != nil {
		t.Fatal(err)
	}
	page := f.read(t, f.b.Token, "", 100)
	b, _ = json.Marshal(page.Messages[0])
	fields = nil
	_ = json.Unmarshal(b, &fields)
	if fields["priority"] != "urgent" {
		t.Fatalf("priority not retained: %s", b)
	}
}
