package stack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func mailReviewSetup(t *testing.T) (taskReviewFixture, Receipt) {
	t.Helper()

	f := taskReviewSetup(t)
	task := f.create(t, "retained shared mail")
	for _, actor := range []int{1, 2} {
		task = f.change(t, 0, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: task.Revision, AgentID: f.agents[actor].ResourceID})
	}

	return f, task
}

func TestMailSpecOpeningQuoteVersusQuotedEmail(t *testing.T) {
	f, task := mailReviewSetup(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name, body string
		mentions   int
	}{
		{"ordinary-email", "Write to person@review-worker-b.example", 0},
		{"opening-quote-mention", `Ask "@review-worker-b"`, 1},
		{"quoted-local-email", `Write to "person"@review-worker-b.example`, 0},
		{"bracketed-quoted-email", `Contact <"person"@review-worker-b.example>`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receipt, err := f.service.Store.SendMail(ctx, f.creds[1].Token, MailSendRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, Body: tc.body})
			if err != nil {
				t.Fatal(err)
			}

			page, err := f.service.Store.ReadMail(ctx, f.creds[2].Token, MailReadRequest{Context: f.ref, TaskID: task.ResourceID})
			if err != nil {
				t.Fatal(err)
			}

			for _, message := range page.Messages {
				if message.ID == receipt.ResourceID && len(message.MentionIDs) != tc.mentions {
					t.Errorf("mention recipients=%d, want=%d", len(message.MentionIDs), tc.mentions)
				}
			}

			var pending int
			if err := f.service.Store.db.QueryRow("SELECT count(*) FROM mail_mentions WHERE message_id=? AND notice_state='pending'", receipt.ResourceID).Scan(&pending); err != nil {
				t.Fatal(err)
			}

			if pending != tc.mentions {
				t.Errorf("pending notices=%d, want=%d", pending, tc.mentions)
			}
		})
	}
}

func TestMailSpecAuthorityRetentionAndSuppression(t *testing.T) {
	ctx := context.Background()
	f, task := mailReviewSetup(t)

	s := f.service.Store
	browser := issueTest(t, s, BrowserCapability, opaqueID(), f.ref.SessionID)
	for _, c := range []Credential{f.op, browser, f.creds[0]} {
		_, err := s.ReadMail(ctx, c.Token, MailReadRequest{Context: f.ref, TaskID: task.ResourceID})
		if c.Kind == BrowserCapability {
			// The Session's browser view reads Task mail, read-only.
			if err != nil {
				t.Fatal("browser view mail read", err)
			}
		} else {
			reviewCode(t, err, "forbidden")
		}

		_, err = s.SendMail(ctx, c.Token, MailSendRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, Body: "not an assignee"})
		reviewCode(t, err, "forbidden")
	}

	for _, c := range []Credential{f.op, browser} {
		_, err := s.CheckMail(ctx, c.Token, MailCheckRequest{Context: f.ref})
		reviewCode(t, err, "forbidden")

		path := filepath.Join(f.service.lock.dir, string(c.Kind)+"-mail-denied.json")
		if err := writePrivate(path, c, false); err != nil {
			t.Fatal(err)
		}

		client, err := NewClient(f.service.lock.dir, path, c.Kind)
		if err != nil {
			t.Fatal(err)
		}

		for _, route := range []struct {
			path string
			body any
		}{
			{"/v1/mail/send", MailSendRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, Body: "unauthorized HTTP mail"}},
			{"/v1/mail/check", MailCheckRequest{Context: f.ref}},
		} {
			_, err := client.Do(ctx, "POST", route.path, route.body)
			reviewCode(t, err, "forbidden")
		}

		_, err = client.Do(ctx, "POST", "/v1/mail/read", MailReadRequest{Context: f.ref, TaskID: task.ResourceID})
		if c.Kind == BrowserCapability {
			if err != nil {
				t.Fatal("browser view HTTP mail read", err)
			}
		} else {
			reviewCode(t, err, "forbidden")
		}
	}

	request := MailSendRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, Body: "mail-only-marker @review-worker-b (@review-worker-a)", Mentions: []string{"review-worker-b", "review-worker-b"}}

	original, err := s.SendMail(ctx, f.creds[1].Token, request)
	if err != nil {
		t.Fatal(err)
	}

	retry, err := s.SendMail(ctx, f.creds[1].Token, request)
	if err != nil || retry != original || original.BodyRef != original.ResourceID || original.Revision != 1 {
		t.Fatal("lost-response retry duplicated or changed mail")
	}

	changed := request
	changed.Body = "changed payload"
	_, err = s.SendMail(ctx, f.creds[1].Token, changed)
	reviewCode(t, err, "operation_conflict")

	if f.show(t, task.ResourceID).Revision != task.Revision {
		t.Fatal("append-only mail changed Task revision")
	}

	var pending, self int
	if err := s.db.QueryRow("SELECT count(*) FROM mail_mentions WHERE notice_state='pending'").Scan(&pending); err != nil {
		t.Fatal(err)
	}

	if err := s.db.QueryRow("SELECT count(*) FROM mail_mentions WHERE notice_state='self'").Scan(&self); err != nil || pending != 1 || self != 1 {
		t.Fatal("dedup/self suppression failed")
	}

	firstA, err := s.ReadMail(ctx, f.creds[1].Token, MailReadRequest{Context: f.ref, TaskID: task.ResourceID})
	if err != nil {
		t.Fatal(err)
	}

	firstB, err := s.ReadMail(ctx, f.creds[2].Token, MailReadRequest{Context: f.ref, TaskID: task.ResourceID})
	if err != nil || len(firstA.Messages) != 1 || len(firstB.Messages) != 1 || firstA.Messages[0].ID != firstB.Messages[0].ID {
		t.Fatal("reader consumed shared history")
	}

	newRecipient := Credential{Kind: AgentCapability, Token: opaqueID()}

	_, err = s.BindAgent(ctx, f.op.Token, "rebind", AgentBindingRequest{Context: f.ref, OperationID: opaqueID(), AgentID: f.agents[2].ResourceID, ExpectedRevision: 1, CredentialHash: tokenHash(newRecipient.Token)})
	if err != nil {
		t.Fatal(err)
	}

	f.creds[2] = newRecipient
	fromB := MailSendRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, Body: "second sender retained history"}

	_, err = s.SendMail(ctx, f.creds[2].Token, fromB)
	if err != nil {
		t.Fatal(err)
	}

	continued, err := s.ReadMail(ctx, f.creds[1].Token, MailReadRequest{Context: f.ref, TaskID: task.ResourceID, Cursor: firstA.Cursor})
	if err != nil || len(continued.Messages) != 1 || continued.Messages[0].Sequence != 2 {
		t.Fatal("caller-held continuation skipped/replayed content")
	}

	task = f.change(t, 0, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: task.Revision, AgentID: f.agents[0].ResourceID})
	late, err := s.ReadMail(ctx, f.creds[0].Token, MailReadRequest{Context: f.ref, TaskID: task.ResourceID})
	if err != nil || len(late.Messages) != 2 || len(late.Messages[0].MentionIDs) != 2 {
		t.Fatal("late join/rebind lost retained IDs/history")
	}

	wantIDs := []string{f.agents[1].ResourceID, f.agents[2].ResourceID}
	sort.Strings(wantIDs)

	if fmt.Sprint(late.Messages[0].MentionIDs) != fmt.Sprint(wantIDs) {
		t.Fatal("credential rebind changed resolved mention identities")
	}

	task = f.change(t, 0, "unassign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: task.Revision, AgentID: f.agents[2].ResourceID})
	_, err = s.ReadMail(ctx, f.creds[2].Token, MailReadRequest{Context: f.ref, TaskID: task.ResourceID})
	reviewCode(t, err, "forbidden")
	_, err = s.SendMail(ctx, f.creds[2].Token, fromB)
	reviewCode(t, err, "forbidden")

	checked, err := s.CheckMail(ctx, f.creds[2].Token, MailCheckRequest{Context: f.ref, Cursors: map[string]string{task.ResourceID: firstB.Cursor}})
	if err != nil || len(checked.Tasks) != 0 {
		t.Fatal("mail check exposed a removed Task")
	}

	var suppressed int
	if err := s.db.QueryRow("SELECT count(*) FROM mail_mentions WHERE recipient_id=? AND notice_state='suppressed'", f.agents[2].ResourceID).Scan(&suppressed); err != nil || suppressed != 1 {
		t.Fatal("removal did not suppress old pending mention")
	}

	_, err = s.SendMail(ctx, f.creds[1].Token, MailSendRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, Body: "message during absence"})
	if err != nil {
		t.Fatal(err)
	}

	task = f.change(t, 0, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: task.Revision, AgentID: f.agents[2].ResourceID})
	rejoined, err := s.ReadMail(ctx, f.creds[2].Token, MailReadRequest{Context: f.ref, TaskID: task.ResourceID})
	if err != nil || len(rejoined.Messages) != 3 {
		t.Fatal("rejoin failed to restore interval history")
	}

	if err := s.db.QueryRow("SELECT count(*) FROM mail_mentions WHERE recipient_id=? AND notice_state='suppressed'", f.agents[2].ResourceID).Scan(&suppressed); err != nil || suppressed != 1 {
		t.Fatal("rejoin resurrected old pending notice")
	}

	task = f.change(t, 1, "status", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: task.Revision, Progress: "finished", Summary: "whole Task complete"})
	_, err = s.ChangeSession(ctx, f.op.Token, "close", SessionChangeRequest{Context: f.ref, OperationID: opaqueID(), ExpectedRevision: 2})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.SendMail(ctx, f.creds[1].Token, MailSendRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, Body: "new closed send"})
	reviewCode(t, err, "session_closed")

	recovered, err := s.SendMail(ctx, f.creds[1].Token, request)
	if err != nil || recovered != original {
		t.Fatal("closed authorized retry lost original receipt")
	}

	closed, err := s.ReadMail(ctx, f.creds[2].Token, MailReadRequest{Context: f.ref, TaskID: task.ResourceID})
	if err != nil || len(closed.Messages) != 3 {
		t.Fatal("closed/finished Task lost authorized mail")
	}

	public, err := s.ShowTask(ctx, f.op.Token, TaskReadRequest{Context: f.ref, TaskID: task.ResourceID})
	encoded, _ := json.Marshal(public)
	if err != nil || bytes.Contains(encoded, []byte("mail-only-marker")) {
		t.Fatal("mail leaked into public Task detail")
	}

	dir := f.service.lock.dir
	if err := f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}

	restarted, _ := reviewService(t, dir)
	recovered, err = restarted.Store.SendMail(ctx, f.creds[1].Token, request)
	if err != nil || recovered != original {
		t.Fatal("restart lost original receipt")
	}

	page, err := restarted.Store.ReadMail(ctx, f.creds[2].Token, MailReadRequest{Context: f.ref, TaskID: task.ResourceID})
	if err != nil || len(page.Messages) != 3 {
		t.Fatal("restart lost retained mail")
	}
}

func TestMailSpecAtomicOutboxAndReceiptRollback(t *testing.T) {
	ctx := context.Background()
	f, task := mailReviewSetup(t)

	s := f.service.Store
	request := MailSendRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, Body: "atomic @review-worker-b"}
	for _, table := range []string{"mail_mentions", "operation_receipts"} {
		if _, err := s.db.Exec("CREATE TRIGGER review_fail BEFORE INSERT ON " + table + " BEGIN SELECT RAISE(ABORT,'injected mail failure'); END"); err != nil {
			t.Fatal(err)
		}

		result, err := s.SendMail(ctx, f.creds[1].Token, request)
		reviewCode(t, err, "storage_unavailable")

		if result != (Receipt{}) {
			t.Fatal("failure returned a success receipt")
		}

		reviewCount(t, s.db, "mail_messages", 0)
		reviewCount(t, s.db, "mail_mentions", 0)

		var receipts int
		if err := s.db.QueryRow("SELECT count(*) FROM operation_receipts WHERE operation_id=?", request.OperationID).Scan(&receipts); err != nil || receipts != 0 {
			t.Fatal("partial failure stored a receipt")
		}

		if _, err := s.db.Exec("DROP TRIGGER review_fail"); err != nil {
			t.Fatal(err)
		}
	}

	committed, err := s.SendMail(ctx, f.creds[1].Token, request)
	if err != nil || committed.Revision != 1 {
		t.Fatal("rollback consumed a sequence or prevented retry")
	}

	bad := request
	bad.OperationID = opaqueID()
	bad.Mentions = []string{"not-a-current-assignee"}
	_, err = s.SendMail(ctx, f.creds[1].Token, bad)
	reviewCode(t, err, "invalid_input")
	reviewCount(t, s.db, "mail_messages", 1)
	reviewCount(t, s.db, "mail_mentions", 1)
}

func TestMailSpecSequenceCursorAndEncodedBounds(t *testing.T) {
	ctx := context.Background()

	f, task := mailReviewSetup(t)
	s := f.service.Store
	for i := range 520 {
		_, err := s.SendMail(ctx, f.creds[1].Token, MailSendRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, Body: fmt.Sprintf("message %d", i)})
		if err != nil {
			t.Fatal(err)
		}
	}

	results := make(chan error, 16)
	for i := range 16 {
		go func(i int) {
			_, err := s.SendMail(ctx, f.creds[1].Token, MailSendRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, Body: fmt.Sprintf("concurrent %d", i)})
			results <- err
		}(i)
	}

	for range 16 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}

	first, err := s.ReadMail(ctx, f.creds[1].Token, MailReadRequest{Context: f.ref, TaskID: task.ResourceID})
	if err != nil || len(first.Messages) != 100 || !first.More {
		t.Fatal("default mail page limit")
	}

	all, err := s.ReadMail(ctx, f.creds[2].Token, MailReadRequest{Context: f.ref, TaskID: task.ResourceID, Limit: 500})
	if err != nil || len(all.Messages) != 500 || !all.More {
		t.Fatal("maximum mail page limit")
	}

	for i, message := range all.Messages {
		if message.Sequence != int64(i+1) {
			t.Fatal("concurrent sends broke sequence order")
		}
	}

	rest, err := s.ReadMail(ctx, f.creds[2].Token, MailReadRequest{Context: f.ref, TaskID: task.ResourceID, Cursor: all.Cursor, Limit: 500})
	if err != nil || len(rest.Messages) != 36 || rest.More {
		t.Fatal("page continuation lost concurrent mail")
	}

	for _, cursor := range []string{"malformed-cursor", encodeMailCursor(opaqueID(), 1), encodeMailCursor(task.ResourceID, 99999), encodeMailCursor(task.ResourceID, -1)} {
		_, err = s.ReadMail(ctx, f.creds[2].Token, MailReadRequest{Context: f.ref, TaskID: task.ResourceID, Cursor: cursor})
		reviewCode(t, err, "invalid_input")
	}

	for _, limit := range []int{-1, 501} {
		_, err = s.ReadMail(ctx, f.creds[2].Token, MailReadRequest{Context: f.ref, TaskID: task.ResourceID, Limit: limit})
		reviewCode(t, err, "invalid_input")
	}

	for _, body := range []string{strings.Repeat("x", (64<<10)+1), string([]byte{255})} {
		_, err = s.SendMail(ctx, f.creds[1].Token, MailSendRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, Body: body})
		reviewCode(t, err, "invalid_input")
	}

	large := f.create(t, "large mail")

	large = f.change(t, 0, "assign", TaskChangeRequest{TaskID: large.ResourceID, ExpectedRevision: 1, AgentID: f.agents[1].ResourceID})
	body := strings.Repeat("\x00", 64<<10)
	for range 2 {
		if _, err := s.SendMail(ctx, f.creds[1].Token, MailSendRequest{Context: f.ref, OperationID: opaqueID(), TaskID: large.ResourceID, Body: body}); err != nil {
			t.Fatal(err)
		}
	}

	client, err := NewClient(f.service.lock.dir, "", OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Do(ctx, "POST", "/v1/mail/read", MailReadRequest{Context: f.ref, TaskID: large.ResourceID})
	reviewCode(t, err, "forbidden")

	path := filepath.Join(f.service.lock.dir, "mail-reader.json")
	if err := writePrivate(path, f.creds[1], false); err != nil {
		t.Fatal(err)
	}

	client, err = NewClient(f.service.lock.dir, path, AgentCapability)
	if err != nil {
		t.Fatal(err)
	}

	cursor := ""
	for i := range 2 {
		data, err := client.Do(ctx, "POST", "/v1/mail/read", MailReadRequest{Context: f.ref, TaskID: large.ResourceID, Cursor: cursor, Limit: 500})
		if err != nil || len(data) > 1<<20 {
			t.Fatalf("escaped mail exceeds client bound: %v", err)
		}

		var page struct{ Data MailPage }
		if json.Unmarshal(data, &page) != nil || len(page.Data.Messages) != 1 || page.Data.Messages[0].Body != body || page.Data.Messages[0].Sequence != int64(i+1) || page.Data.More != (i == 0) {
			t.Fatal("byte cap skipped or truncated a message")
		}

		cursor = page.Data.Cursor
		t.Logf("escaped maximum-body page %d: %d bytes", i+1, len(data))
	}
}

func TestMailSpecCheckFairSweepAndIndependentPositions(t *testing.T) {
	ctx := context.Background()
	f, first := mailReviewSetup(t)

	s := f.service.Store
	ids := []string{first.ResourceID}
	for range 2 {
		task := f.create(t, "another mailbox")
		f.change(t, 0, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: 1, AgentID: f.agents[1].ResourceID})
		ids = append(ids, task.ResourceID)
	}

	sort.Strings(ids)

	body := strings.Repeat("\x00", 64<<10)
	for i, id := range ids {
		n := 1
		if i == 0 {
			n = 3
		}

		for range n {
			if _, err := s.SendMail(ctx, f.creds[1].Token, MailSendRequest{Context: f.ref, OperationID: opaqueID(), TaskID: id, Body: body}); err != nil {
				t.Fatal(err)
			}
		}
	}

	cursors := map[string]string{}
	more := map[string]bool{}
	seen := map[string]bool{}

	trace := []string{}
	after := ""
	for calls := 0; calls < 20; calls++ {
		page, err := s.CheckMail(ctx, f.creds[1].Token, MailCheckRequest{Context: f.ref, Cursors: cursors, AfterTask: after, Limit: 500, TaskLimit: 100})
		encoded, encodeErr := json.Marshal(page)
		if err != nil || encodeErr != nil || len(encoded) > 1<<20 {
			t.Fatalf("mail-check aggregate bound: %v", err)
		}

		for _, task := range page.Tasks {
			for _, message := range task.Messages {
				if seen[message.ID] {
					t.Fatal("check replayed an advanced caller cursor")
				}

				seen[message.ID] = true
				trace = append(trace, task.TaskID)
			}

			cursors[task.TaskID] = task.Cursor
			more[task.TaskID] = task.More
		}

		after = page.NextTask
		if calls == 0 && after != "" {
			_, err := s.CheckMail(ctx, f.creds[2].Token, MailCheckRequest{Context: f.ref, AfterTask: after})
			reviewCode(t, err, "invalid_input")
		}

		pending := false
		for _, value := range more {
			pending = pending || value
		}

		if after == "" && !pending {
			break
		}

		if calls == 19 {
			t.Fatal("mail check did not finish the caller-held sweep")
		}
	}

	if len(seen) != 5 || len(trace) < 3 || trace[0] != ids[0] || trace[1] != ids[1] || trace[2] != ids[2] {
		t.Fatal("busy first Task starved later Tasks or lost messages")
	}
}

func TestMailSpecSendClosureAndMembershipRaces(t *testing.T) {
	for _, action := range []string{"close", "remove-sender", "remove-recipient"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			f, task := mailReviewSetup(t)
			s := f.service.Store
			// Observe authority at the actual insert; this trigger does not reject
			// invalid writes or substitute for the production checks.
			_, err := s.db.Exec(`CREATE TABLE review_mail_audit(closed INTEGER, sender_member INTEGER, recipient_member INTEGER);
CREATE TRIGGER review_mail_insert AFTER INSERT ON mail_messages BEGIN
 INSERT INTO review_mail_audit SELECT ss.closed,
  (SELECT count(*) FROM task_assignees WHERE task_id=NEW.task_id AND agent_id=NEW.sender_id),
  (SELECT count(*) FROM task_assignees WHERE task_id=NEW.task_id AND agent_id='` + f.agents[2].ResourceID + `')
 FROM tasks t JOIN sessions ss ON ss.id=t.session_id WHERE t.id=NEW.task_id;
END;`)
			if err != nil {
				t.Fatal(err)
			}

			r := MailSendRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, Body: "racing @review-worker-b"}
			start := make(chan struct{})
			type outcome struct {
				receipt Receipt
				err     error
			}
			sent := make(chan outcome, 1)
			changed := make(chan error, 1)
			go func() {
				<-start
				receipt, err := s.SendMail(ctx, f.creds[1].Token, r)
				sent <- outcome{receipt, err}
			}()
			go func() {
				<-start

				var err error
				if action == "close" {
					_, err = s.ChangeSession(ctx, f.op.Token, "close", SessionChangeRequest{Context: f.ref, OperationID: opaqueID(), ExpectedRevision: 2})
				} else {
					actor := 1
					if action == "remove-recipient" {
						actor = 2
					}

					_, err = s.ChangeTask(ctx, f.creds[0].Token, "unassign", TaskChangeRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, ExpectedRevision: task.Revision, AgentID: f.agents[actor].ResourceID})
				}

				changed <- err
			}()
			close(start)

			result, changeErr := <-sent, <-changed
			if changeErr != nil {
				t.Fatal(changeErr)
			}

			count := 1
			if result.err != nil {
				code := map[string]string{"close": "session_closed", "remove-sender": "forbidden", "remove-recipient": "invalid_input"}[action]
				reviewCode(t, result.err, code)
				count = 0
			} else if result.receipt.Revision != 1 {
				t.Fatal("racing send did not receive first sequence")
			}

			reviewCount(t, s.db, "mail_messages", count)
			reviewCount(t, s.db, "mail_mentions", count)

			var invalid int
			if err := s.db.QueryRow("SELECT count(*) FROM review_mail_audit WHERE closed<>0 OR sender_member<>1 OR recipient_member<>1").Scan(&invalid); err != nil || invalid != 0 {
				t.Fatal("send committed without open Session/current sender and recipient")
			}

			if action == "remove-recipient" {
				var pending int
				if err := s.db.QueryRow("SELECT count(*) FROM mail_mentions WHERE notice_state='pending'").Scan(&pending); err != nil || pending != 0 {
					t.Fatal("racing removal left an old pending notice")
				}
			}
		})
	}
}
