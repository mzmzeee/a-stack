package stack

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func waitReceiptExpiry(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("receipt expiry did not run")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestExpiredOperationCannotApplyBeforeOrAfterCleanup(t *testing.T) {
	s, c := transactionFixture(t)
	waitReceiptExpiry(t, func() bool { return s.nextReceiptExpiry.Load() > clockNow().Unix() })
	op := Operation{ID: "expiry-original", Name: "test.create", Scope: "session", Payload: json.RawMessage(`{}`)}
	original, err := s.Mutate(ctx, c.Token, op, replayAccess, testApply("once", false))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Mutate(ctx, c.Token, op, replayAccess, testApply("twice", false)); err != nil || got != original {
		t.Fatalf("young replay: %+v %v", got, err)
	}
	at := clockNow()
	execTest(t, s, "UPDATE operation_receipts SET received_at=? WHERE operation_id=?", at.Add(-ReceiptAge-time.Second).UTC().Format(time.RFC3339Nano), op.ID)
	neverReplay := func(context.Context, *sql.Tx, Principal, Receipt) error {
		t.Error("expired receipt reached typed replay")
		return nil
	}
	neverApply := func(context.Context, *sql.Tx, Principal) (Receipt, error) {
		t.Error("expired operation applied again")
		return Receipt{}, nil
	}
	for _, prune := range []bool{false, true} {
		if prune {
			n, err := s.expireReceiptBatch(ctx, at)
			if err != nil || n != 1 {
				t.Fatalf("expiry %d %v", n, err)
			}
		}
		for _, payload := range []string{`{}`, `{"changed":true}`} {
			request := op
			request.Payload = json.RawMessage(payload)
			got, err := s.Mutate(ctx, c.Token, request, neverReplay, neverApply)
			requireCode(t, err, "operation_expired")
			if got != (Receipt{}) || publicError(err).Retryable {
				t.Fatal("expired ID disclosed receipt or is retryable")
			}
			if !strings.Contains(err.Error(), "not applied again") {
				t.Fatal(err)
			}
		}
	}
	if countTest(t, s, "SELECT count(*) FROM test_objects") != 1 || countTest(t, s, "SELECT count(*) FROM test_history") != 1 || countTest(t, s, "SELECT count(*) FROM operation_receipts") != 0 {
		t.Fatal("expiry changed retained effects")
	}
	var outcome string
	if s.db.QueryRow("SELECT outcome FROM expired_operations WHERE operation_id=?", op.ID).Scan(&outcome) != nil || outcome != "created" {
		t.Fatal("result kind not retained")
	}
	// Authentication precedes even the existence-only expired-ID check.
	_, err = s.Mutate(ctx, "invalid", op, neverReplay, neverApply)
	requireCode(t, err, "unauthorized")
	// Another actor may use the same ID; it cannot see the first actor's receipt.
	other := issueTest(t, s, AgentCapability, "other-agent", "session")
	if _, err = s.Mutate(ctx, other.Token, op, replayAccess, func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		_, err := tx.ExecContext(ctx, "INSERT INTO test_objects VALUES('other','separate actor')")
		return Receipt{ResourceID: "other", Revision: 1, Outcome: "created"}, err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReceiptExpirySmallBatchesBoundaryAndAtomicity(t *testing.T) {
	service, _, _ := startTest(t)
	s := service.Store
	waitReceiptExpiry(t, func() bool { return s.nextReceiptExpiry.Load() > clockNow().Unix() })
	at := clockNow().UTC().Truncate(time.Second)
	old := at.Add(-ReceiptAge - time.Second).Format(time.RFC3339Nano)
	execTest(t, s, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<450)
 INSERT INTO operation_receipts SELECT 'actor',printf('bulk-%08d',i),'test','scope','digest','resource',1,'created','',? FROM n`, old)
	execTest(t, s, "CREATE TRIGGER fail_expiry_delete BEFORE DELETE ON operation_receipts BEGIN SELECT RAISE(ABORT,'injected'); END")
	if _, err := s.expireReceiptBatch(ctx, at); err == nil {
		t.Fatal("expiry accepted failed delete")
	}
	if countTest(t, s, "SELECT count(*) FROM operation_receipts") != 450 || countTest(t, s, "SELECT count(*) FROM expired_operations") != 0 {
		t.Fatal("partial expiry committed")
	}
	execTest(t, s, "DROP TRIGGER fail_expiry_delete")
	for _, want := range []int{200, 200, 50, 0} {
		n, err := s.expireReceiptBatch(ctx, at)
		if err != nil || n != want {
			t.Fatalf("batch %d want %d: %v", n, want, err)
		}
	}
	cutoff := at.Add(-ReceiptAge)
	for i, timestamp := range []time.Time{cutoff, cutoff.Add(time.Nanosecond), at} {
		execTest(t, s, "INSERT INTO operation_receipts VALUES('actor',?,'test','scope','digest','resource',1,'created','',?)", fmt.Sprintf("boundary-%d", i), timestamp.Format(time.RFC3339Nano))
	}
	if n, err := s.expireReceiptBatch(ctx, at); err != nil || n != 1 {
		t.Fatalf("boundary batch %d: %v", n, err)
	}
	if countTest(t, s, "SELECT count(*) FROM operation_receipts") != 2 || countTest(t, s, "SELECT count(*) FROM expired_operations") != 451 {
		t.Fatal("age boundary lost a young receipt")
	}
	status, err := s.Status()
	if err != nil || status.ReceiptCount != 2 || status.ExpiredOperationCount != 451 || status.NextReceiptExpiryAt == "" {
		t.Fatalf("status %+v %v", status, err)
	}
	next, err := time.Parse(time.RFC3339, status.NextReceiptExpiryAt)
	if err != nil || next.Before(at.Add(23*time.Hour)) || next.After(at.Add(25*time.Hour)) {
		t.Fatal("next expiry is not daily")
	}
}

func TestReceiptExpiryOnStartAndDaily(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	waitReceiptExpiry(t, func() bool { return s.nextReceiptExpiry.Load() > clockNow().Unix() })
	count := countTest(t, s, "SELECT count(*) FROM operation_receipts")
	path, state := s.path, f.service.lock.dir
	if err := f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	execFile(t, path, "UPDATE operation_receipts SET received_at='2000-01-01T00:00:00Z'")
	restarted := serveTest(t, state, "127.0.0.1:0")
	s = restarted.Store
	waitReceiptExpiry(t, func() bool { return countTest(t, s, "SELECT count(*) FROM expired_operations") == count })
	if countTest(t, s, "SELECT count(*) FROM operation_receipts") != 0 {
		t.Fatal("start retained old receipts")
	}
	waitReceiptExpiry(t, func() bool { return s.nextReceiptExpiry.Load() > clockNow().Unix() })
	// Exercise the same daily loop with a short interval, not a 24-hour sleep.
	s.nextReceiptExpiry.Store(0)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); s.runReceiptExpiry(runCtx, 20*time.Millisecond, time.Millisecond) }()
	defer func() { cancel(); <-done }()
	waitReceiptExpiry(t, func() bool { return s.nextReceiptExpiry.Load() != 0 })
	execTest(t, s, "INSERT INTO operation_receipts VALUES('actor','daily-expired','test','scope','digest','resource',1,'created','','2000-01-01T00:00:00Z')")
	waitReceiptExpiry(t, func() bool {
		return countTest(t, s, "SELECT count(*) FROM expired_operations WHERE operation_id='daily-expired'") == 1
	})
	if countTest(t, s, "SELECT count(*) FROM operation_receipts WHERE operation_id='daily-expired'") != 0 {
		t.Fatal("daily pass retained old receipt")
	}
}

func TestExpiredMailHTTPAndRetainedHistory(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	waitReceiptExpiry(t, func() bool { return s.nextReceiptExpiry.Load() > clockNow().Unix() })
	id := f.create(t, "Mail").ResourceID
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: id, ExpectedRevision: 1, AgentID: f.aID})
	request := MailSendRequest{Context: f.ref(0), TaskID: id, OperationID: "expired-http-mail", Body: "Retain this body."}
	if _, err := s.SendMail(ctx, f.a.Token, request); err != nil {
		t.Fatal(err)
	}
	execTest(t, s, "UPDATE operation_receipts SET received_at='2000-01-01T00:00:00Z' WHERE operation_id=?", request.OperationID)
	if _, err := s.expireReceiptBatch(ctx, clockNow()); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(request)
	status, response := rawRequest(t, f.service, f.a, "POST", "/v1/mail/send", strings.NewReader(string(body)), map[string]string{"Content-Type": "application/json"})
	if status != 409 || !strings.Contains(string(response), `"code":"operation_expired"`) || strings.Contains(string(response), `"data"`) {
		t.Fatalf("HTTP %d %s", status, response)
	}
	page, err := s.ReadMail(ctx, f.a.Token, MailReadRequest{Context: f.ref(0), TaskID: id})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Body != request.Body {
		t.Fatalf("retained mail %+v %v", page, err)
	}
	state := f.service.lock.dir
	if err = f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	restarted := serveTest(t, state, "127.0.0.1:0")
	if _, err = restarted.Store.SendMail(ctx, f.a.Token, request); err == nil {
		t.Fatal("restart forgot expired ID")
	}
	requireCode(t, err, "operation_expired")
	if countTest(t, restarted.Store, "SELECT count(*) FROM mail_messages") != 1 {
		t.Fatal("restart duplicated mail")
	}
}
