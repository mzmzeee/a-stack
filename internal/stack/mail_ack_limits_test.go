package stack

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestMailAckMaximumResponseAndSummaryOverflow(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	names := []string{"03-worker"}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	for i := 1; i < 500; i++ {
		id := opaqueID()

		name := fmt.Sprintf("%060d-%03d", i, i)
		names = append(names, name)
		if _, err = tx.Exec("INSERT INTO agents(id,workspace_id,session_id,name,created_at,updated_at) VALUES(?,?,?,?,?,?)", id, f.ref(0).WorkspaceID, f.ref(0).SessionID, name, now(), now()); err != nil {
			t.Fatal(err)
		}

		if _, err = tx.Exec("INSERT INTO task_assignees(task_id,agent_id,added_by,added_at,session_id) VALUES(?,?,?,?,?)", f.taskID, id, f.orchID, now(), f.ref(0).SessionID); err != nil {
			t.Fatal(err)
		}
	}

	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	body := strings.Repeat("\x00", MaxMailBodyBytes)
	for range 2 {
		_, err = s.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: opaqueID(), Body: body, AckRequired: true, AckFrom: names, AckDue: "1h"})
		if err != nil {
			t.Fatal(err)
		}
	}

	cursor := ""
	for i := range 2 {
		page, err := s.ReadMail(ctx, f.b.Token, MailReadRequest{Context: f.ref(0), TaskID: f.taskID, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}

		data, _ := json.Marshal(page)
		if len(data) > mailResponseBudget || len(page.Messages) != 1 || len(page.Messages[0].Acks) != 500 || len(page.PendingAcks) != 500 || !page.MoreAcks || page.Messages[0].Body != body || page.Messages[0].Sequence != int64(i+1) {
			t.Fatalf("invalid bounded page (%d bytes): messages=%d pending=%d more=%v", len(data), len(page.Messages), len(page.PendingAcks), page.MoreAcks)
		}

		cursor = page.Cursor
	}

	check := ackCheck(t, f, f.b.Token)
	data, _ := json.Marshal(check)
	if len(data) > mailResponseBudget || len(check.PendingAcks) != 2 {
		t.Fatal("check bounds", len(data))
	}
}
