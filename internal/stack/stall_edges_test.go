package stack

import (
	"testing"
	"time"
)

func TestUnreadMentionAgentAndNotificationProjection(t *testing.T) {
	f := mailFixture(t)
	f.send(t, f.a.Token, "Question @03-worker")

	status, err := f.service.notificationStatus(ctx, f.b.Token, NoticeRequest{Context: f.ref(0)})
	if err != nil || len(status.Targets) != 1 || len(status.Targets[0].MailWaits) != 1 {
		t.Fatal(status, err)
	}

	page, err := f.service.Store.SnapshotPage(ctx, f.a.Token, SnapshotPageRequest{Context: f.ref(0), Kind: "agents"})
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, item := range page.Items {
		a := item.(SnapshotAgent)
		if a.ID == f.bID {
			found = true

			if len(a.MailWaits) != 1 || a.MailWaits[0].RecipientName != "03-worker" {
				t.Fatal(a)
			}
		}
	}

	if !found {
		t.Fatal("recipient missing")
	}
}

func TestUnreadMentionReadEvidenceAndRollback(t *testing.T) {
	f := mailFixture(t)
	f.send(t, f.a.Token, "Question @03-worker")
	_, err := f.service.Store.ReadMail(ctx, f.b.Token, MailReadRequest{Context: f.ref(0), TaskID: f.taskID, Cursor: "bad"})
	requireCode(t, err, "invalid_input")

	if countTest(t, f.service.Store, "SELECT count(*) FROM agents WHERE last_mail_read_at IS NOT NULL") != 0 {
		t.Fatal("failed read left evidence")
	}

	execTest(t, f.service.Store, `CREATE TRIGGER fail_own_read BEFORE UPDATE OF last_mail_read_at ON agents BEGIN SELECT RAISE(ABORT,'read failure'); END;`)
	_, err = f.service.Store.CheckMail(ctx, f.b.Token, MailCheckRequest{Context: f.ref(0)})
	requireCode(t, err, "storage_unavailable")
	execTest(t, f.service.Store, "DROP TRIGGER fail_own_read")

	if countTest(t, f.service.Store, "SELECT count(*) FROM agents WHERE last_mail_read_at IS NOT NULL") != 0 {
		t.Fatal("rollback left read evidence")
	}

	if _, err = f.service.Store.CheckMail(ctx, f.b.Token, MailCheckRequest{Context: f.ref(0)}); err != nil {
		t.Fatal(err)
	}

	if countTest(t, f.service.Store, "SELECT count(*) FROM agents WHERE last_mail_read_at IS NOT NULL") != 1 {
		t.Fatal("disabled integration lost own read")
	}
}

func TestHookPeekDoesNotClearUnreadMention(t *testing.T) {
	f := mailFixture(t)
	f.send(t, f.a.Token, "Question @03-worker")

	if _, err := f.service.Store.CheckMail(ctx, f.b.Token, MailCheckRequest{Context: f.ref(0), Peek: true}); err != nil {
		t.Fatal(err)
	}

	if countTest(t, f.service.Store, "SELECT count(*) FROM agents WHERE last_mail_read_at IS NOT NULL") != 0 {
		t.Fatal("hook peek recorded an own read")
	}

	detail, err := f.service.Store.ShowTask(ctx, f.a.Token, TaskReadRequest{Context: f.ref(0), TaskID: f.taskID})
	if err != nil || len(detail.MailWaits) != 1 {
		t.Fatal("hook peek cleared unread wait", detail, err)
	}
}

func TestUnreadMentionAgeChangesSnapshotWithoutMutation(t *testing.T) {
	f := mailFixture(t)
	at := time.Now().UTC()
	fixedClock.Store(&at)
	t.Cleanup(func() { fixedClock.Store(nil) })
	f.send(t, f.a.Token, "Question @03-worker")

	first, err := f.service.Store.Snapshot(ctx, f.a.Token, SnapshotRequest{Context: f.ref(0)})
	if err != nil {
		t.Fatal(err)
	}

	later := at.Add(MailStalledAfter)
	fixedClock.Store(&later)

	after, err := f.service.Store.Snapshot(ctx, f.a.Token, SnapshotRequest{Context: f.ref(0), KnownRevision: first.Session.ReadRevision, hasKnownMark: true, knownMark: first.archiveMark})
	if err != nil || after.Unchanged {
		t.Fatal("age did not invalidate snapshot", after, err)
	}

	detail, err := f.service.Store.ShowTask(ctx, f.a.Token, TaskReadRequest{Context: f.ref(0), TaskID: f.taskID})
	if err != nil || len(detail.MailWaits) != 1 || !detail.MailWaits[0].Stalled {
		t.Fatal(detail, err)
	}

	if _, err = f.service.Store.CheckMail(ctx, f.b.Token, MailCheckRequest{Context: f.ref(0)}); err != nil {
		t.Fatal(err)
	}

	detail, err = f.service.Store.ShowTask(ctx, f.a.Token, TaskReadRequest{Context: f.ref(0), TaskID: f.taskID})
	if err != nil || len(detail.MailWaits) != 0 {
		t.Fatal("own read did not clear wait", detail, err)
	}
}

func TestOrchestratorOwnReadClearsReachabilityWarning(t *testing.T) {
	f := mailFixture(t)
	before, err := f.service.Store.InspectSession(ctx, f.orch.Token, f.ref(0))
	if err != nil || before.OrchestratorWarning == "" {
		t.Fatal(before, err)
	}

	if _, err = f.service.Store.CheckMail(ctx, f.orch.Token, MailCheckRequest{Context: f.ref(0)}); err != nil {
		t.Fatal(err)
	}

	after, err := f.service.Store.InspectSession(ctx, f.orch.Token, f.ref(0))
	if err != nil || after.OrchestratorWarning != "" {
		t.Fatal(after, err)
	}

	old := time.Now().Add(-16 * time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err = f.service.Store.db.Exec("UPDATE agents SET last_mail_read_at=? WHERE id=?", old, *after.OrchestratorID); err != nil {
		t.Fatal(err)
	}

	after, err = f.service.Store.InspectSession(ctx, f.orch.Token, f.ref(0))
	if err != nil || after.OrchestratorWarning == "" {
		t.Fatal(after, err)
	}
}

func TestOrchestratorReachabilityExpiresAtBoundary(t *testing.T) {
	for _, column := range []string{"observed_at", "last_mail_read_at"} {
		t.Run(column, func(t *testing.T) {
			f := mailFixture(t)
			at := time.Now().UTC()
			fixedClock.Store(&at)
			t.Cleanup(func() { fixedClock.Store(nil) })

			session, err := f.service.Store.InspectSession(ctx, f.orch.Token, f.ref(0))
			if err != nil {
				t.Fatal(err)
			}

			if _, err = f.service.Store.db.Exec("UPDATE agents SET "+column+"=? WHERE id=?", at.Format(time.RFC3339Nano), *session.OrchestratorID); err != nil {
				t.Fatal(err)
			}

			first, err := f.service.Store.Snapshot(ctx, f.orch.Token, SnapshotRequest{Context: f.ref(0)})
			if err != nil || first.Session.OrchestratorWarning != "" {
				t.Fatal(first, err)
			}

			later := at.Add(OrchestratorReachabilityAfter - time.Nanosecond)
			fixedClock.Store(&later)

			session, err = f.service.Store.InspectSession(ctx, f.orch.Token, f.ref(0))
			if err != nil || session.OrchestratorWarning != "" {
				t.Fatal("recent evidence expired early", session, err)
			}

			later = at.Add(OrchestratorReachabilityAfter)
			fixedClock.Store(&later)

			after, err := f.service.Store.Snapshot(ctx, f.orch.Token, SnapshotRequest{Context: f.ref(0), KnownRevision: first.Session.ReadRevision, hasKnownMark: true, knownMark: first.archiveMark})
			if err != nil || after.Unchanged || after.Session.OrchestratorWarning == "" {
				t.Fatal("expired evidence did not invalidate snapshot", after, err)
			}
		})
	}
}

func TestUnreadExtraCancelledBeforePreflight(t *testing.T) {
	f := noticeTestFixture(t)
	clock := time.Now()
	f.n.clock = func() time.Time { return clock }
	f.send(t, f.a.Token, "Question @03-worker")
	f.drain(t, 4)
	clock = clock.Add(11 * time.Minute)

	target, err := f.n.nextTarget(ctx, f.ref(0).SessionID)
	if err != nil {
		t.Fatal(err)
	}

	batch, err := f.n.claim(ctx, target)
	if err != nil || batch.id == "" || !batch.extra {
		t.Fatal(batch, err)
	}

	f.read(t, f.b.Token, "", 50)

	state, reason, err := f.n.preflight(ctx, batch)
	if err != nil || state != NoticeSuppressed || reason != "mail_checked" {
		t.Fatal(state, reason, err)
	}

	if err = f.n.finish(batch, state, reason); err != nil {
		t.Fatal(err)
	}

	if f.drain(t, 4).Submitted != 0 || f.prompts(t) != 1 {
		t.Fatal("read extra was submitted")
	}
}

func TestUnreadExtraKnownFailureStillChecksOwnRead(t *testing.T) {
	f := noticeTestFixture(t)
	clock := time.Now()
	f.n.clock = func() time.Time { return clock }
	f.send(t, f.a.Token, "Question @03-worker")
	f.drain(t, 4)
	clock = clock.Add(11 * time.Minute)

	target, err := f.n.nextTarget(ctx, f.ref(0).SessionID)
	if err != nil {
		t.Fatal(err)
	}

	batch, err := f.n.claim(ctx, target)
	if err != nil || !batch.extra {
		t.Fatal(batch, err)
	}

	if err = f.n.finish(batch, NoticePending, "runtime_blocked"); err != nil {
		t.Fatal(err)
	}

	f.read(t, f.b.Token, "", 50)
	clock = clock.Add(time.Minute)

	if got := f.drain(t, 4); got.Checked != 0 || f.prompts(t) != 1 {
		t.Fatal("read retried extra", got)
	}
}
