package stack

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// requireMessage checks an invalid_input error and the words it must name.
func requireMessage(t *testing.T, err error, want string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != "invalid_input" || e.Message != want {
		t.Fatalf("error = %v, want invalid_input %q", err, want)
	}
}

func TestErrorsNameTheFieldAndOperationIDRule(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	for _, id := range []string{"short", "", "has space!", strings.Repeat("x", 129)} {
		_, err := s.ChangeTask(ctx, f.a.Token, "report", TaskChangeRequest{Context: f.ref(0), OperationID: id, TaskID: f.taskID, ExpectedRevision: 3})
		requireMessage(t, err, "operation ID must be 8–128 letters, digits, _ or -")
	}
	_, err := PrepareAgentCredential(filepath.Join(t.TempDir(), "a.json"), opaqueID(), "agent register", "reg1", nil)
	requireMessage(t, err, OperationIDRule)
	_, err = PrepareAgentCredential("relative.json", opaqueID(), "agent register", "register-1", nil)
	requireMessage(t, err, "new credential file must be an absolute file name")

	status := func(progress, reason, summary string) error {
		_, err := s.ChangeTask(ctx, f.a.Token, "status", TaskChangeRequest{Context: f.ref(0), OperationID: opaqueID(), TaskID: f.taskID, ExpectedRevision: 3, Progress: progress, Reason: reason, Summary: summary})
		return err
	}
	requireMessage(t, status("waiting", " ", ""), "reason is required")
	requireMessage(t, status("finished", "", ""), "summary is required")
	requireMessage(t, status("cancelled", strings.Repeat("x", MaxTaskNoteBytes+1), ""), "reason exceeds 2048 bytes")
	requireMessage(t, status("working", "\xff", ""), "reason is not valid UTF-8")
	_, err = s.CreateTask(ctx, f.orch.Token, TaskCreateRequest{Context: f.ref(0), OperationID: opaqueID()})
	requireMessage(t, err, "title is required")

	_, err = s.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: opaqueID(), Body: "hi @nobody, @03-worker and @ghost"})
	requireMessage(t, err, "mention target must be a current Task assignee: @ghost, @nobody are not current assignees of this Task; assignees: 02-worker, 03-worker; run mail recipients --task "+f.taskID+"; write other names without @")
	_, err = s.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: opaqueID(), Body: "hi", Mentions: []string{"01-orchestrator"}})
	requireMessage(t, err, "mention target must be a current Task assignee: @01-orchestrator is not a current assignee of this Task; assignees: 02-worker, 03-worker; run mail recipients --task "+f.taskID+"; write other names without @")
	_, err = s.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: opaqueID()})
	requireMessage(t, err, "body is required")
}
