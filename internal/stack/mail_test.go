package stack

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
)

type mailTeam struct {
	taskTeam
	taskID string
}

func mailFixture(t *testing.T) mailTeam {
	t.Helper()
	f := tasksFixture(t)
	task := f.create(t, "Shared mail")
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: 1, AgentID: f.aID})
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: 2, AgentID: f.bID})
	return mailTeam{f, task.ResourceID}
}
func (f mailTeam) send(t *testing.T, token, body string, mentions ...string) Receipt {
	t.Helper()
	r, err := f.service.Store.SendMail(ctx, token, MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: opaqueID(), Body: body, Mentions: mentions})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func (f mailTeam) read(t *testing.T, token, cursor string, limit int) MailPage {
	t.Helper()
	r, err := f.service.Store.ReadMail(ctx, token, MailReadRequest{Context: f.ref(0), TaskID: f.taskID, Cursor: cursor, Limit: limit})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestMailIndependentReadersReplayAndClosedHistory(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	snapshot, _ := s.Snapshot(ctx, f.a.Token, SnapshotRequest{Context: f.ref(0)})
	firstRequest := MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: "mail-lost-response", Body: "mail-private-body @03-worker"}
	first, err := s.SendMail(ctx, f.a.Token, firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 5; i++ {
		r := f.send(t, f.a.Token, fmt.Sprintf("Message %d", i), "03-worker")
		if r.Revision != int64(i) {
			t.Fatal("per-Task sequence")
		}
	}
	retry, err := s.SendMail(ctx, f.a.Token, firstRequest)
	if err != nil || retry != first || first.BodyRef != first.ResourceID || first.Revision != 1 {
		t.Fatal("retry changed original receipt")
	}
	changed := firstRequest
	changed.Body = "Changed body"
	_, err = s.SendMail(ctx, f.a.Token, changed)
	requireCode(t, err, "operation_conflict")
	a := f.read(t, f.a.Token, "", 2)
	b := f.read(t, f.b.Token, "", 2)
	if len(a.Messages) != 2 || len(b.Messages) != 2 || a.Messages[0].ID != b.Messages[0].ID || a.Cursor != b.Cursor || !a.More {
		t.Fatal("read consumed another assignee's history")
	}
	aNext := f.read(t, f.a.Token, a.Cursor, 2)
	if aNext.Messages[0].Sequence != 3 || f.read(t, f.b.Token, "", 2).Messages[0].Sequence != 1 {
		t.Fatal("caller positions are not independent")
	}
	if f.read(t, f.a.Token, "", 0).Messages[0].ID != first.ResourceID {
		t.Fatal("lost cursor silently skipped history")
	}
	_, err = s.ReadMail(ctx, f.operator.Token, MailReadRequest{Context: f.ref(0), TaskID: f.taskID})
	requireCode(t, err, "forbidden")
	_, err = s.ReadMail(ctx, f.orch.Token, MailReadRequest{Context: f.ref(0), TaskID: f.taskID})
	requireCode(t, err, "forbidden")
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 3, AgentID: f.orchID})
	late := f.read(t, f.orch.Token, "", 100)
	if len(late.Messages) != 5 || late.Messages[0].SenderID != f.aID {
		t.Fatal("late assignee lost retained history")
	}
	f.change(t, f.a.Token, "status", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 4, Progress: "finished", Summary: "Task finished"})
	f.send(t, f.a.Token, "Mail after whole-Task completion")
	current, _ := s.Snapshot(ctx, f.a.Token, SnapshotRequest{Context: f.ref(0)})
	if current.Session.ReadRevision != snapshot.Session.ReadRevision+2+6 { // assign, status and six sends; reads add nothing
		t.Fatal("mail/read created browser revisions or read state")
	}
	_, err = s.ChangeSession(ctx, f.operator.Token, "close", SessionChangeRequest{Context: f.ref(0), OperationID: opaqueID(), ExpectedRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.read(t, f.b.Token, "", 100).Messages) != 6 {
		t.Fatal("closed history unavailable")
	}
	retry, err = s.SendMail(ctx, f.a.Token, firstRequest)
	if err != nil || retry != first {
		t.Fatal("closed receipt recovery")
	}
	_, err = s.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: opaqueID(), Body: "Closed write"})
	requireCode(t, err, "session_closed")
	if countTest(t, s, "SELECT count(*) FROM mail_messages") != 6 || countTest(t, s, "SELECT count(*) FROM mail_mentions") != 5 {
		t.Fatal("retry duplicated bodies/mentions")
	}
}
func TestMailRemovalReplayRejoinAndPermanentSuppression(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	first := f.send(t, f.a.Token, "@02-worker and @03-worker", "03-worker", "03-worker")
	senderRequest := MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: "removed-sender-retry", Body: "Reply @02-worker"}
	senderReceipt, err := s.SendMail(ctx, f.b.Token, senderRequest)
	if err != nil {
		t.Fatal(err)
	}
	old := f.read(t, f.b.Token, "", 1).Cursor
	f.change(t, f.orch.Token, "unassign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 3, AgentID: f.bID})
	for _, request := range []MailReadRequest{{Context: f.ref(0), TaskID: f.taskID}, {Context: f.ref(0), TaskID: f.taskID, Cursor: old}} {
		_, err = s.ReadMail(ctx, f.b.Token, request)
		requireCode(t, err, "forbidden")
	}
	_, err = s.SendMail(ctx, f.b.Token, senderRequest)
	requireCode(t, err, "forbidden")
	_, err = s.SendMail(ctx, f.b.Token, MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: opaqueID(), Body: "Removed write"})
	requireCode(t, err, "forbidden")
	check, err := s.CheckMail(ctx, f.b.Token, MailCheckRequest{Context: f.ref(0), Cursors: map[string]string{f.taskID: old}})
	if err != nil || len(check.Tasks) != 0 {
		t.Fatal("mail check leaked removed Task")
	}
	var state NoticeState
	if s.db.QueryRow("SELECT notice_state FROM mail_mentions WHERE message_id=? AND recipient_id=?", first.ResourceID, f.bID).Scan(&state) != nil || state != NoticeSuppressed {
		t.Fatal("pending notice not suppressed atomically")
	}
	f.send(t, f.a.Token, "Message while recipient is away")
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 4, AgentID: f.bID})
	if len(f.read(t, f.b.Token, "", 100).Messages) != 3 {
		t.Fatal("rejoin did not restore the interval away")
	}
	recovered, err := s.SendMail(ctx, f.b.Token, senderRequest)
	if err != nil || recovered != senderReceipt {
		t.Fatal("rejoined assignee could not recover own receipt")
	}
	pending := f.send(t, f.a.Token, "New mention @03-worker")
	if s.db.QueryRow("SELECT notice_state FROM mail_mentions WHERE message_id=? AND recipient_id=?", first.ResourceID, f.bID).Scan(&state) != nil || state != NoticeSuppressed {
		t.Fatal("rejoin resurrected old notice")
	}
	if s.db.QueryRow("SELECT notice_state FROM mail_mentions WHERE message_id=? AND recipient_id=?", pending.ResourceID, f.bID).Scan(&state) != nil || state != NoticePending {
		t.Fatal("new mention not pending")
	}
	execTest(t, s, "UPDATE mail_mentions SET notice_state=? WHERE message_id=? AND recipient_id=?", NoticeSubmitting, pending.ResourceID, f.bID)
	f.change(t, f.orch.Token, "unassign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 5, AgentID: f.bID})
	if s.db.QueryRow("SELECT notice_state FROM mail_mentions WHERE message_id=? AND recipient_id=?", pending.ResourceID, f.bID).Scan(&state) != nil || state != NoticeSuppressed {
		t.Fatal("captured submission not invalidated on removal")
	}
	if countTest(t, s, "SELECT count(*) FROM mail_messages") != 4 {
		t.Fatal("removal deleted shared history")
	}
}
func TestMailMentionBoundariesValidationAndSelfNotification(t *testing.T) {
	cases := []struct {
		body string
		want []string
	}{
		{"@one end", []string{"one"}}, {"x@one.example", nil}, {"one@example.org", nil},
		{"(@one) [@two] {@three} <@four> \"@five\" '@six' “@seven”", []string{"five", "four", "one", "seven", "six", "three", "two"}},
		{"x,@one x:@two x/@three", nil}, {"\n@one\u2003@two", []string{"one", "two"}}, {"@one-tail @one_under @one.tail", []string{"one", "one-tail", "one_under"}},
	}
	for _, tc := range cases {
		names, err := mentionNames(tc.body, nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(names, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("mention grammar for %q: %v", tc.body, names)
		}
	}
	f := mailFixture(t)
	s := f.service.Store
	first := f.send(t, f.a.Token, "@02-worker (@03-worker), again @03-worker", "03-worker", "03-worker")
	page := f.read(t, f.a.Token, "", 100)
	if len(page.Messages[0].MentionIDs) != 2 {
		t.Fatal("resolved mentions duplicated or self attribution lost")
	}
	var self, pending int
	s.db.QueryRow("SELECT count(*) FROM mail_mentions WHERE message_id=? AND notice_state=?", first.ResourceID, NoticeSelf).Scan(&self)
	s.db.QueryRow("SELECT count(*) FROM mail_mentions WHERE message_id=? AND notice_state=?", first.ResourceID, NoticePending).Scan(&pending)
	if self != 1 || pending != 1 {
		t.Fatal("self mention produced a notification")
	}
	for _, body := range []string{"@01-orchestrator is not assigned", "@03-worker_extra is not the exact name", "@" + strings.Repeat("a", 65), strings.Repeat("x", MaxMailBodyBytes+1), string([]byte{255}), "  "} {
		_, err := s.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: opaqueID(), Body: body})
		requireCode(t, err, "invalid_input")
	}
	_, err := s.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: opaqueID(), Body: "Explicit unknown", Mentions: []string{"missing"}})
	requireCode(t, err, "invalid_input")
	if countTest(t, s, "SELECT count(*) FROM mail_messages") != 1 {
		t.Fatal("invalid send partially committed")
	}
	duplicates := make([]string, 1000)
	for i := range duplicates {
		duplicates[i] = "03-worker"
	}
	f.send(t, f.a.Token, "Explicit duplicate names", duplicates...)
	if countTest(t, s, "SELECT count(*) FROM mail_mentions") != 3 {
		t.Fatal("explicit duplicates were not deduplicated")
	}
}
func TestMailConcurrentSequencesAndTransactionalFailures(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	var wg sync.WaitGroup
	results := make(chan Receipt, 32)
	failures := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Go(func() {
			r, err := s.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: opaqueID(), Body: "Concurrent send @03-worker"})
			if err != nil {
				failures <- err
			} else {
				results <- r
			}
		})
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	seen := map[int64]bool{}
	for r := range results {
		if seen[r.Revision] || r.Revision < 1 || r.Revision > 32 {
			t.Fatal("duplicate/out-of-range sequence")
		}
		seen[r.Revision] = true
	}
	if len(seen) != 32 {
		t.Fatal("concurrent sends missing")
	}
	for _, table := range []string{"mail_mentions", "operation_receipts"} {
		execTest(t, s, "CREATE TRIGGER fail_mail_write BEFORE INSERT ON "+table+" BEGIN SELECT RAISE(ABORT,'injected failure');END")
		req := MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: "failure-" + table, Body: "Atomic @03-worker"}
		_, err := s.SendMail(ctx, f.a.Token, req)
		requireCode(t, err, "storage_unavailable")
		if countTest(t, s, "SELECT count(*) FROM mail_messages") != 32 || countTest(t, s, "SELECT count(*) FROM mail_mentions") != 32 {
			t.Fatal("partial body/mention after failure")
		}
		var n int
		if s.db.QueryRow("SELECT count(*) FROM operation_receipts WHERE operation_id=?", req.OperationID).Scan(&n) != nil || n != 0 {
			t.Fatal("failure acknowledged a receipt")
		}
		execTest(t, s, "DROP TRIGGER fail_mail_write")
	}
	r := f.send(t, f.a.Token, "Capacity recovered")
	if r.Revision != 33 {
		t.Fatal("rollback consumed a Task sequence")
	}
}
func TestMailClosureAndRemovalRaces(t *testing.T) {
	for _, boundary := range []string{"close", "remove"} {
		t.Run(boundary, func(t *testing.T) {
			f := mailFixture(t)
			s := f.service.Store
			start := make(chan struct{})
			sent := make(chan error, 1)
			changed := make(chan error, 1)
			request := MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: "racing-mail-send", Body: "Race @03-worker"}
			go func() { <-start; _, err := s.SendMail(ctx, f.b.Token, request); sent <- err }()
			go func() {
				<-start
				var err error
				if boundary == "close" {
					_, err = s.ChangeSession(ctx, f.operator.Token, "close", SessionChangeRequest{Context: f.ref(0), OperationID: opaqueID(), ExpectedRevision: 2})
				} else {
					_, err = s.ChangeTask(ctx, f.orch.Token, "unassign", TaskChangeRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: opaqueID(), ExpectedRevision: 3, AgentID: f.bID})
				}
				changed <- err
			}()
			close(start)
			sendErr, changeErr := <-sent, <-changed
			if changeErr != nil {
				t.Fatal(changeErr)
			}
			if sendErr != nil {
				code := "session_closed"
				if boundary == "remove" {
					code = "forbidden"
				}
				requireCode(t, sendErr, code)
				if countTest(t, s, "SELECT count(*) FROM mail_messages") != 0 || countTest(t, s, "SELECT count(*) FROM mail_mentions") != 0 {
					t.Fatal("rejected race retained partial state")
				}
			}
			if boundary == "remove" {
				_, err := s.ReadMail(ctx, f.b.Token, MailReadRequest{Context: f.ref(0), TaskID: f.taskID})
				requireCode(t, err, "forbidden")
				if countTest(t, s, "SELECT count(*) FROM mail_mentions WHERE notice_state='pending' AND recipient_id='"+f.bID+"'") != 0 {
					t.Fatal("removed recipient still pending")
				}
			} else {
				if _, err := s.ReadMail(ctx, f.a.Token, MailReadRequest{Context: f.ref(0), TaskID: f.taskID}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestMailMembershipRemovalFailureIsAtomic(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	f.send(t, f.a.Token, "@03-worker")
	execTest(t, s, "CREATE TRIGGER reject_suppression BEFORE UPDATE ON mail_mentions BEGIN SELECT RAISE(ABORT,'injected suppression failure'); END")
	_, err := s.ChangeTask(ctx, f.orch.Token, "unassign", TaskChangeRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: "failed-unassign-mail", ExpectedRevision: 3, AgentID: f.bID})
	requireCode(t, err, "storage_unavailable")
	if len(f.read(t, f.b.Token, "", 100).Messages) != 1 || countTest(t, s, "SELECT count(*) FROM mail_mentions WHERE notice_state='pending'") != 1 || f.show(t, f.taskID).Revision != 3 {
		t.Fatal("suppression failure partially removed assignment")
	}
}
func TestMailHTTPPrivacyCurrentCredentialsAndReadLedgerAbsence(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	marker := "private-mail-body-never-in-browser"
	request := MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: "private-message-send", Body: marker}
	_, err := s.SendMail(ctx, f.a.Token, request)
	if err != nil {
		t.Fatal(err)
	}
	browser := issueTest(t, s, BrowserCapability, "view", f.sessions[0])
	view := clientForCredential(t, f.taskTeam, browser)
	for _, path := range []string{"/v1/mail/check", "/v1/mail/send"} {
		_, err = view.Do(ctx, "POST", path, map[string]any{})
		requireCode(t, err, "forbidden")
	}
	for _, kind := range []string{"tasks", "agents", "assignments", "dependencies"} {
		data, err := view.Do(ctx, "POST", "/v1/session/page", SnapshotPageRequest{Context: f.ref(0), Kind: kind})
		if err != nil || bytes.Contains(data, []byte(marker)) {
			t.Fatal("mail body leaked through snapshot")
		}
	}
	data, err := view.Do(ctx, "POST", "/v1/task/show", TaskReadRequest{Context: f.ref(0), TaskID: f.taskID})
	if err != nil || bytes.Contains(data, []byte(marker)) {
		t.Fatal("mail body leaked through detail")
	}
	newCredential := Credential{Kind: AgentCapability, Token: opaqueID()}
	_, err = s.BindAgent(ctx, f.operator.Token, "rebind", AgentBindingRequest{Context: f.ref(0), AgentID: f.aID, OperationID: opaqueID(), ExpectedRevision: 1, CredentialHash: tokenHash(newCredential.Token)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ReadMail(ctx, f.a.Token, MailReadRequest{Context: f.ref(0), TaskID: f.taskID})
	requireCode(t, err, "unauthorized")
	_, err = s.SendMail(ctx, f.a.Token, request)
	requireCode(t, err, "unauthorized")
	recovered, err := s.SendMail(ctx, newCredential.Token, request)
	if err != nil || recovered.Revision != 1 {
		t.Fatal("current credential could not recover stable-identity receipt")
	}
	before := countTest(t, s, "SELECT count(*) FROM operation_receipts")
	for i := 0; i < 3; i++ {
		_, err = s.ReadMail(ctx, newCredential.Token, MailReadRequest{Context: f.ref(0), TaskID: f.taskID})
		if err != nil {
			t.Fatal(err)
		}
	}
	if countTest(t, s, "SELECT count(*) FROM operation_receipts") != before {
		t.Fatal("read stored a receipt/ledger")
	}
	var tables string
	rows, err := s.db.Query("SELECT name FROM sqlite_schema WHERE type='table' AND name LIKE 'mail_%'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		rows.Scan(&name)
		tables += name + ","
	}
	if strings.Contains(tables, "cursor") || strings.Contains(tables, "unread") || strings.Contains(tables, "read_receipt") {
		t.Fatal("server read ledger added")
	}
}

func TestMailQuotedEmailClosuresDoNotMention(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	for _, body := range []string{`Contact "person"@03-worker.example`, `<"person"@03-worker.example>`, `Contact 'person'@03-worker.example`, `person'@03-worker.example`, `"person "@03-worker.example`} {
		f.send(t, f.a.Token, body)
	}
	if countTest(t, s, "SELECT count(*) FROM mail_mentions") != 0 {
		t.Fatal("email closing quote became an opening-quote mention")
	}
	for _, body := range []string{`Ask "@03-worker"`, `Ask:'@03-worker'`, `Ask:"@03-worker"`, `The worker's note says '@03-worker'`} {
		f.send(t, f.a.Token, body)
	}
	if countTest(t, s, "SELECT count(*) FROM mail_mentions WHERE notice_state='pending'") != 4 {
		t.Fatal("opening quote mention controls stopped working")
	}
}

// The browser view reads the mail of any Task in its own Session, read-only,
// with the same pages and cursor as mail read (ADR 0003 amendment).
func TestBrowserMailReadScopePagingAndMailCount(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	other := f.create(t, "No mail")
	for i := 1; i <= 5; i++ {
		f.send(t, f.a.Token, fmt.Sprintf("Line %d\n<script>alert(1)</script> @03-worker", i))
	}
	browser := issueTest(t, s, BrowserCapability, "mail-view", f.sessions[0])
	foreign := issueTest(t, s, BrowserCapability, "foreign-view", f.sessions[1])
	view := clientForCredential(t, f.taskTeam, browser)
	page, err := s.SnapshotPage(ctx, browser.Token, SnapshotPageRequest{Context: f.ref(0), Kind: "tasks"})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	for _, item := range page.Items {
		counts[item.(Task).ID] = item.(Task).MailCount
	}
	if counts[f.taskID] != 5 || counts[other.ResourceID] != 0 {
		t.Fatal("mail_count", counts)
	}
	state := func() string {
		return fmt.Sprint(countTest(t, s, "SELECT read_revision FROM sessions WHERE id='"+f.sessions[0]+"'"),
			countTest(t, s, "SELECT count(*) FROM operation_receipts"),
			countTest(t, s, "SELECT count(*) FROM mail_mentions WHERE notice_state='pending'"))
	}
	before := state()
	var got []MailMessage
	cursor := ""
	for pages := 1; ; pages++ {
		data, err := view.Do(ctx, "POST", "/v1/mail/read", MailReadRequest{Context: f.ref(0), TaskID: f.taskID, Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			Data MailPage `json:"data"`
		}
		if err = decodeJSON(data, &response); err != nil {
			t.Fatal(err)
		}
		got = append(got, response.Data.Messages...)
		cursor = response.Data.Cursor
		if !response.Data.More {
			if pages != 3 {
				t.Fatal("pages", pages)
			}
			break
		}
	}
	if len(got) != 5 || got[0].Sequence != 1 || got[4].Sequence != 5 || got[0].SenderName != "02-worker" || !strings.Contains(got[0].Body, "<script>") || len(got[0].MentionIDs) != 1 {
		t.Fatal("browser mail pages", got)
	}
	if state() != before {
		t.Fatal("browser mail read changed state")
	}
	empty, err := s.ReadMail(ctx, browser.Token, MailReadRequest{Context: f.ref(0), TaskID: other.ResourceID})
	if err != nil || len(empty.Messages) != 0 {
		t.Fatal("empty Task mail", err)
	}
	_, err = s.ReadMail(ctx, foreign.Token, MailReadRequest{Context: f.ref(0), TaskID: f.taskID})
	requireCode(t, err, "forbidden")
	_, err = s.ReadMail(ctx, foreign.Token, MailReadRequest{Context: f.ref(1), TaskID: f.taskID})
	requireCode(t, err, "not_found")
	_, err = s.ReadMail(ctx, browser.Token, MailReadRequest{Context: f.ref(0), TaskID: f.taskID, Cursor: encodeMailCursor(other.ResourceID, 1)})
	requireCode(t, err, "invalid_input")
	for path, body := range map[string]any{
		"/v1/mail/send":   MailSendRequest{Context: f.ref(0), OperationID: opaqueID(), TaskID: f.taskID, Body: "view write"},
		"/v1/mail/check":  MailCheckRequest{Context: f.ref(0)},
		"/v1/task/report": TaskChangeRequest{Context: f.ref(0), OperationID: opaqueID(), TaskID: f.taskID, ExpectedRevision: 3},
		"/v1/task/create": TaskCreateRequest{Context: f.ref(0), OperationID: opaqueID(), Title: "view write"},
	} {
		_, err = view.Do(ctx, "POST", path, body)
		requireCode(t, err, "forbidden")
	}
	_, err = s.ReadMail(ctx, f.operator.Token, MailReadRequest{Context: f.ref(0), TaskID: f.taskID})
	requireCode(t, err, "forbidden")
}
