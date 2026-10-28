package stack

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestReplyScopeMembershipAndRetry(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	parent := f.send(t, f.a.Token, "Private parent")
	request := MailReplyRequest{MailSendRequest: MailSendRequest{Context: f.ref(0), OperationID: "reply-scope-01", Body: "Answer"}, MessageID: parent.ResourceID}
	first, err := s.ReplyMail(ctx, f.b.Token, request)
	if err != nil || first.TaskID != f.taskID || first.TaskTitle != "Shared mail" {
		t.Fatalf("reply: %+v %v", first, err)
	}
	page := f.read(t, f.b.Token, "", 100)
	if len(page.Messages) != 2 || len(page.Messages[1].MentionIDs) != 1 || page.Messages[1].MentionIDs[0] != f.aID {
		t.Fatalf("default sender mention: %+v", page)
	}
	again, err := s.ReplyMail(ctx, f.b.Token, request)
	if err != nil || again != first {
		t.Fatalf("retry: %+v %v", again, err)
	}
	changed := request
	changed.Body = "Changed"
	_, err = s.ReplyMail(ctx, f.b.Token, changed)
	requireCode(t, err, "operation_conflict")
	changed = request
	changed.OperationID = opaqueID()
	changed.TaskID = "another-task"
	_, err = s.ReplyMail(ctx, f.b.Token, changed)
	requireCode(t, err, "invalid_input")
	changed = request
	changed.OperationID = opaqueID()
	changed.Context = f.ref(1)
	if _, err = s.ReplyMail(ctx, f.b.Token, changed); err == nil {
		t.Fatal("cross-Session reply accepted")
	}
	for _, token := range []string{f.orch.Token, f.operator.Token} {
		_, err = s.ReplyMail(ctx, token, request)
		requireCode(t, err, "forbidden")
		_, err = s.MailRecipients(ctx, token, MailRecipientsRequest{Context: f.ref(0), TaskID: f.taskID})
		requireCode(t, err, "forbidden")
	}
	f.change(t, f.orch.Token, "unassign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 3, AgentID: f.bID})
	_, err = s.ReplyMail(ctx, f.b.Token, request)
	requireCode(t, err, "forbidden")
	changed = request
	changed.OperationID = opaqueID()
	_, err = s.ReplyMail(ctx, f.b.Token, changed)
	requireCode(t, err, "forbidden")
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 4, AgentID: f.bID})
	again, err = s.ReplyMail(ctx, f.b.Token, request)
	if err != nil || again != first {
		t.Fatal("rejoin replay", err)
	}
	// Removing the parent sender does not invalidate an already committed reply.
	f.change(t, f.orch.Token, "unassign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 5, AgentID: f.aID})
	again, err = s.ReplyMail(ctx, f.b.Token, request)
	if err != nil || again != first {
		t.Fatal("sender removal changed retry", err)
	}
	changed = request
	changed.OperationID = opaqueID()
	if _, err = s.ReplyMail(ctx, f.b.Token, changed); err != nil {
		t.Fatal("reply to former assignee", err)
	}
	page = f.read(t, f.b.Token, "", 100)
	if len(page.Messages) != 3 || len(page.Messages[2].MentionIDs) != 0 {
		t.Fatal("removed sender mentioned")
	}
	recipients, err := s.MailRecipients(ctx, f.b.Token, MailRecipientsRequest{Context: f.ref(0), TaskID: f.taskID})
	if err != nil || len(recipients.Recipients) != 1 || recipients.Recipients[0].AgentID != f.bID {
		t.Fatalf("recipients: %+v %v", recipients, err)
	}
	if _, err = s.ChangeSession(ctx, f.operator.Token, "close", SessionChangeRequest{Context: f.ref(0), OperationID: opaqueID(), ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	again, err = s.ReplyMail(ctx, f.b.Token, request)
	if err != nil || again != first {
		t.Fatal("closed replay", err)
	}
	changed.OperationID = opaqueID()
	_, err = s.ReplyMail(ctx, f.b.Token, changed)
	requireCode(t, err, "session_closed")
}

func TestReplyMentionsAndHTTP(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	parent := f.send(t, f.a.Token, "Parent")
	request := MailReplyRequest{MailSendRequest: MailSendRequest{Context: f.ref(0), OperationID: opaqueID(), Body: "Without automatic mention"}, MessageID: parent.ResourceID, NoMentionSender: true}
	raw, _ := json.Marshal(request)
	status, response := rawRequest(t, f.service, f.b, "POST", "/v1/mail/reply", bytes.NewReader(raw), map[string]string{"Content-Type": "application/json"})
	if status != 200 || !strings.Contains(string(response), "shared with all current assignees") {
		t.Fatalf("HTTP reply: %d %s", status, response)
	}
	if len(f.read(t, f.b.Token, "", 100).Messages[1].MentionIDs) != 0 {
		t.Fatal("opt-out ignored")
	}
	request.OperationID = opaqueID()
	request.Mentions = []string{"ghost"}
	_, err := s.ReplyMail(ctx, f.b.Token, request)
	requireCode(t, err, "invalid_input")
	if !strings.Contains(err.Error(), "mail recipients --task") {
		t.Fatal("no recipient repair hint", err)
	}
	if countTest(t, s, "SELECT count(*) FROM mail_messages") != 2 {
		t.Fatal("invalid mention partially sent")
	}
	request.Mentions = []string{"03-worker"}
	request.NoMentionSender = false
	if _, err = s.ReplyMail(ctx, f.b.Token, request); err != nil {
		t.Fatal(err)
	}
	if len(f.read(t, f.b.Token, "", 100).Messages[2].MentionIDs) != 2 {
		t.Fatal("explicit mention lost")
	}
	// Operator answers have no Agent sender to mention.
	execTest(t, s, "UPDATE mail_messages SET sender_kind='operator',sender_id=NULL WHERE id=?", parent.ResourceID)
	request.OperationID = opaqueID()
	request.Mentions = nil
	if _, err = s.ReplyMail(ctx, f.b.Token, request); err != nil {
		t.Fatal("Operator parent", err)
	}
	if len(f.read(t, f.b.Token, "", 100).Messages[3].MentionIDs) != 0 {
		t.Fatal("Operator mentioned as Agent")
	}
	request.MessageID = "missing-message"
	request.OperationID = opaqueID()
	_, err = s.ReplyMail(ctx, f.b.Token, request)
	requireCode(t, err, "not_found")
}
