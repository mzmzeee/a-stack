package stack

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestMailCursorsLimitsAndByteBoundaries(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	for i := 0; i < 501; i++ {
		f.send(t, f.a.Token, "Small message")
	}
	first := f.read(t, f.b.Token, "", 0)
	if len(first.Messages) != 100 || !first.More {
		t.Fatal("default page limit")
	}
	maximum := f.read(t, f.b.Token, "", 500)
	if len(maximum.Messages) != 500 || !maximum.More {
		t.Fatal("maximum page limit")
	}
	last := f.read(t, f.b.Token, maximum.Cursor, 500)
	if len(last.Messages) != 1 || last.Messages[0].Sequence != 501 || last.More {
		t.Fatal("count boundary skipped a message")
	}
	empty := f.read(t, f.b.Token, last.Cursor, 100)
	if len(empty.Messages) != 0 || empty.Cursor != last.Cursor || empty.More {
		t.Fatal("empty read changed caller position")
	}
	for _, limit := range []int{-1, 501} {
		_, err := s.ReadMail(ctx, f.a.Token, MailReadRequest{Context: f.ref(0), TaskID: f.taskID, Limit: limit})
		requireCode(t, err, "invalid_input")
	}
	for _, cursor := range []string{"garbage", encodeMailCursor(opaqueID(), 0), encodeMailCursor(f.taskID, -1), encodeMailCursor(f.taskID, 1000), base64.RawURLEncoding.EncodeToString([]byte(`{"sequence":"bad"}`))} {
		_, err := s.ReadMail(ctx, f.a.Token, MailReadRequest{Context: f.ref(0), TaskID: f.taskID, Cursor: cursor})
		requireCode(t, err, "invalid_input")
	}
	// Worst encoded body plus the maximum resolved mention array still fits as
	// one complete message; byte continuation returns the next unreturned one.
	names := []string{"02-worker", "03-worker"}
	revision := int64(3)
	for i := 0; i < MaxMailMentions-2; i++ {
		name := fmt.Sprintf("mention-%03d", i)
		_, agent := f.identityFixture.register(t, 0, name)
		assigned := f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: revision, AgentID: agent.ResourceID})
		revision = assigned.Revision
		names = append(names, name)
	}
	body := strings.Repeat("<", MaxMailBodyBytes)
	one := f.send(t, f.a.Token, body, names...)
	two := f.send(t, f.a.Token, body, names...)
	client := clientForCredential(t, f.taskTeam, f.a)
	data, err := client.Do(ctx, "POST", "/v1/mail/read", MailReadRequest{Context: f.ref(0), TaskID: f.taskID, Cursor: last.Cursor, Limit: 500})
	if err != nil || len(data) > MaxRequestBytes {
		t.Fatal("encoded mail response exceeds client bound", err)
	}
	var envelope struct {
		Data MailPage `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil || len(envelope.Data.Messages) != 1 || !envelope.Data.More || envelope.Data.Messages[0].ID != one.ResourceID || len(envelope.Data.Messages[0].MentionIDs) != MaxMailMentions || envelope.Data.Messages[0].Body != body {
		t.Fatal("worst encoded single message truncated")
	}
	cursor := envelope.Data.Cursor
	again := f.read(t, f.a.Token, cursor, 500)
	if len(again.Messages) != 1 || again.Messages[0].ID != two.ResourceID || again.More {
		t.Fatal("byte boundary advanced beyond returned message")
	}
	t.Logf("maximum escaped body plus %d resolved recipients: %d-byte complete response", MaxMailMentions, len(data))
}
func TestMailCheckIndependentPositionsAndAggregateBudget(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	tasks := []string{f.taskID}
	for i := 0; i < 2; i++ {
		created := f.create(t, fmt.Sprintf("Mail Task %d", i))
		f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: created.ResourceID, ExpectedRevision: 1, AgentID: f.aID})
		tasks = append(tasks, created.ResourceID)
	}
	for _, task := range tasks {
		for i := 0; i < 2; i++ {
			_, err := s.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: task, OperationID: opaqueID(), Body: strings.Repeat("<", MaxMailBodyBytes)})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	cursors := map[string]string{}
	seen := map[string]bool{}
	// Each round visits current Tasks independently; more in one Task remains
	// behind its own returned cursor and does not disappear when others are read.
	for round := 0; round < 2; round++ {
		after := ""
		visited := map[string]bool{}
		for calls := 0; calls < 10; calls++ {
			before := map[string]string{}
			for key, value := range cursors {
				before[key] = value
			}
			page, err := s.CheckMail(ctx, f.a.Token, MailCheckRequest{Context: f.ref(0), Cursors: cursors, AfterTask: after, Limit: 500, TaskLimit: 100})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, cursors) {
				t.Fatal("server advanced the caller's cursor map")
			}
			encoded, _ := json.Marshal(map[string]any{"data": page})
			if len(encoded) > MaxRequestBytes {
				t.Fatal("aggregate mail response exceeded wire budget")
			}
			for _, task := range page.Tasks {
				visited[task.TaskID] = true
				for _, message := range task.Messages {
					if seen[message.ID] {
						t.Fatal("check replayed a returned message despite saved cursor")
					}
					seen[message.ID] = true
				}
				cursors[task.TaskID] = task.Cursor
			}
			after = page.NextTask
			if after == "" {
				break
			}
			if calls == 9 {
				t.Fatal("Task scan continuation loop")
			}
		}
		if len(visited) != 3 {
			t.Fatal("aggregate budget silently skipped a Task")
		}
	}
	if len(seen) != 6 {
		t.Fatal("aggregate check silently skipped messages")
	}
	noNew, err := s.CheckMail(ctx, f.a.Token, MailCheckRequest{Context: f.ref(0), Cursors: cursors})
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range noNew.Tasks {
		if len(task.Messages) != 0 {
			t.Fatal("saved caller cursor ignored")
		}
	}
	oneTask, err := s.CheckMail(ctx, f.a.Token, MailCheckRequest{Context: f.ref(0), Cursors: cursors, TaskLimit: 1})
	if err != nil || oneTask.NextTask == "" || len(oneTask.Tasks) != 1 {
		t.Fatal("bounded Task enumeration")
	}
	_, err = s.CheckMail(ctx, f.b.Token, MailCheckRequest{Context: f.ref(0), AfterTask: oneTask.NextTask})
	requireCode(t, err, "invalid_input")
	// Start from the saved cursors, so the budget does not end the scan before
	// it reaches the Task with the wrong cursor.
	wrong := map[string]string{}
	for task, cursor := range cursors {
		wrong[task] = cursor
	}
	wrong[f.taskID] = encodeMailCursor(tasks[1], 0)
	_, err = s.CheckMail(ctx, f.a.Token, MailCheckRequest{Context: f.ref(0), Cursors: wrong})
	requireCode(t, err, "invalid_input")
	_, err = s.CheckMail(ctx, f.operator.Token, MailCheckRequest{Context: f.ref(0)})
	requireCode(t, err, "forbidden")
}
func TestMailRawHTTPValidationAndCrossSessionDenial(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	for _, raw := range [][]byte{[]byte(`{"operation_id":"invalid-role","task_id":"x","body":"text","sender_id":"forged"}`), []byte("{\"body\":\"\xff\"}"), bytes.Repeat([]byte("x"), MaxRequestBytes+1)} {
		status, response := rawRequest(t, f.service, f.a, "POST", "/v1/mail/send", bytes.NewReader(raw), map[string]string{"Content-Type": "application/json"})
		var out struct {
			Error *Error `json:"error"`
		}
		if json.Unmarshal(response, &out) != nil || status != 400 || out.Error == nil || out.Error.Code != "invalid_input" {
			t.Fatal("mail malformed/oversized input accepted")
		}
	}
	_, err := s.ReadMail(ctx, f.a.Token, MailReadRequest{Context: f.ref(1), TaskID: f.taskID})
	requireCode(t, err, "forbidden")
	_, err = s.CheckMail(ctx, f.a.Token, MailCheckRequest{Context: f.ref(1)})
	requireCode(t, err, "forbidden")
	if countTest(t, s, "SELECT count(*) FROM mail_messages") != 0 {
		t.Fatal("invalid HTTP request wrote mail")
	}
}

// mail check trusts its assignment query instead of a second per-Task check, so
// a removal between rounds, or between the pages of one round, must still end
// that Task's mail at once. Only the scanned Tasks' cursors are decoded.
func TestMailCheckRemovalBetweenRoundsEndsAccess(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	other := f.create(t, "Second mail Task")
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: other.ResourceID, ExpectedRevision: 1, AgentID: f.bID})
	f.send(t, f.a.Token, "Before removal")
	if _, err := s.SendMail(ctx, f.b.Token, MailSendRequest{Context: f.ref(0), TaskID: other.ResourceID, OperationID: opaqueID(), Body: "Other Task"}); err != nil {
		t.Fatal(err)
	}
	round := func(cursors map[string]string, taskLimit int, between func()) map[string][]MailMessage {
		t.Helper()
		got := map[string][]MailMessage{}
		after := ""
		for calls := 0; calls < 10; calls++ {
			page, err := s.CheckMail(ctx, f.b.Token, MailCheckRequest{Context: f.ref(0), Cursors: cursors, AfterTask: after, TaskLimit: taskLimit})
			if err != nil {
				t.Fatal(err)
			}
			for _, task := range page.Tasks {
				got[task.TaskID] = append(got[task.TaskID], task.Messages...)
				cursors[task.TaskID] = task.Cursor
			}
			if after = page.NextTask; after == "" {
				return got
			}
			if between != nil {
				between()
				between = nil
			}
		}
		t.Fatal("mail check continuation loop")
		return nil
	}
	cursors := map[string]string{}
	if got := round(cursors, 0, nil); len(got[f.taskID]) != 1 || len(got[other.ResourceID]) != 1 {
		t.Fatal("first round missed assigned mail", got)
	}
	revision := func(task string) int64 {
		detail, err := s.ShowTask(ctx, f.orch.Token, TaskReadRequest{Context: f.ref(0), TaskID: task})
		if err != nil {
			t.Fatal(err)
		}
		return detail.Revision
	}
	// Removal between rounds: the saved cursor stays in the caller's map.
	f.change(t, f.orch.Token, "unassign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: revision(f.taskID), AgentID: f.bID})
	f.send(t, f.a.Token, "After removal")
	if got := round(cursors, 0, nil); len(got) != 1 || len(got[other.ResourceID]) != 0 {
		t.Fatal("mail check returned a removed Task or replayed mail", got)
	}
	_, err := s.ReadMail(ctx, f.b.Token, MailReadRequest{Context: f.ref(0), TaskID: f.taskID, Cursor: cursors[f.taskID]})
	requireCode(t, err, "forbidden")
	// The removed Task's cursor is no longer scanned, so it is not decoded.
	stale := map[string]string{f.taskID: "not-a-cursor", other.ResourceID: cursors[other.ResourceID]}
	if got := round(stale, 0, nil); len(got) != 1 {
		t.Fatal("a cursor of an unscanned Task broke mail check", got)
	}
	// A scanned Task's cursor is still checked.
	_, err = s.CheckMail(ctx, f.b.Token, MailCheckRequest{Context: f.ref(0), Cursors: map[string]string{other.ResourceID: encodeMailCursor(f.taskID, 0)}})
	requireCode(t, err, "invalid_input")
	// Rejoin restores the whole retained history.
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: revision(f.taskID), AgentID: f.bID})
	if got := round(map[string]string{}, 0, nil); len(got[f.taskID]) != 2 {
		t.Fatal("rejoin did not restore the history", got)
	}
	// Removal between the pages of one round: the continuation skips the Task.
	ids := []string{f.taskID, other.ResourceID}
	if ids[0] > ids[1] {
		ids[0], ids[1] = ids[1], ids[0]
	}
	got := round(map[string]string{}, 1, func() {
		f.change(t, f.orch.Token, "unassign", TaskChangeRequest{TaskID: ids[1], ExpectedRevision: revision(ids[1]), AgentID: f.bID})
	})
	if _, ok := got[ids[0]]; len(got) != 1 || !ok {
		t.Fatal("removal between pages leaked the removed Task", got)
	}
}
