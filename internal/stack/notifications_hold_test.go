package stack

import (
	"testing"
	"time"
)

func TestNotificationsHoldBurstDespiteOwnRead(t *testing.T) {
	for _, read := range []string{"check", "read"} {
		t.Run(read, func(t *testing.T) {
			f := noticeTestFixture(t)
			clock := time.Now()
			f.n.clock = func() time.Time { return clock }
			for i := 0; i < 20; i++ {
				f.send(t, f.a.Token, "Burst @03-worker")
				f.drain(t, 4)
			}
			if f.prompts(t) != 1 || countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='pending'") != 19 {
				t.Fatal("busy recipient accumulated notices or lost mentions", f.prompts(t))
			}
			// Another Agent and a browser cannot re-arm this recipient.
			f.read(t, f.a.Token, "", 50)
			browser := issueTest(t, f.service.Store, BrowserCapability, opaqueID(), f.ref(0).SessionID)
			f.read(t, browser.Token, "", 50)
			f.drain(t, 4)
			if f.prompts(t) != 1 {
				t.Fatal("foreign read released hold")
			}
			var err error
			if read == "check" {
				_, err = f.service.Store.CheckMail(ctx, f.b.Token, MailCheckRequest{Context: f.ref(0)})
			} else {
				_, err = f.service.Store.ReadMail(ctx, f.b.Token, MailReadRequest{Context: f.ref(0), TaskID: f.taskID})
			}
			if err != nil {
				t.Fatal(err)
			}
			if f.drain(t, 4).Submitted != 0 || f.prompts(t) != 1 {
				t.Fatal("own read bypassed pane rate limit")
			}
			clock = clock.Add(noticeCooldown)
			if f.drain(t, 4).Submitted != 1 || f.prompts(t) != 2 || countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='submitted'") != 20 {
				t.Fatal("cooldown did not release exact pending batch")
			}
		})
	}
}

func TestNotificationsHoldStatusAndFailedRead(t *testing.T) {
	f := noticeTestFixture(t)
	f.service.notifier = f.n
	defer func() { f.service.notifier = nil }()
	f.send(t, f.a.Token, "First @03-worker")
	f.drain(t, 4)
	f.send(t, f.a.Token, "Held @03-worker")
	status, err := f.service.notificationStatus(ctx, f.b.Token, NoticeRequest{Context: f.ref(0)})
	if err != nil || len(status.Targets) != 1 || status.Targets[0].Reason != "notice_waiting_mail" || status.Targets[0].Counts[NoticePending] != 1 || status.Targets[0].NextAttempt == 0 {
		t.Fatal("status hides hold", status, err)
	}
	tx, err := f.service.Store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	item, err := f.service.Store.snapshotItem(ctx, tx, SnapshotPageRequest{Context: f.ref(0), Kind: "agents"}, f.bID)
	tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if a := item.(SnapshotAgent); a.NoticeWarning == nil || a.NoticeWarning.Reason != "notice_waiting_mail" {
		t.Fatal("Agents panel snapshot hides hold", a)
	}
	_, err = f.service.Store.ReadMail(ctx, f.b.Token, MailReadRequest{Context: f.ref(0), TaskID: f.taskID, Cursor: "bad"})
	requireCode(t, err, "invalid_input")
	if f.drain(t, 4).Submitted != 0 {
		t.Fatal("failed read re-armed notices")
	}
	// Eligibility is rechecked in the claim transaction, not just nextTarget.
	target := noticeTarget{agent: f.bID, session: f.ref(0).SessionID, revision: 2, binding: f.config.Binding}
	if err = f.service.Store.db.QueryRow("SELECT binding FROM agents WHERE id=?", f.bID).Scan(&target.bindingJSON); err != nil {
		t.Fatal(err)
	}
	if batch, err := f.n.claim(ctx, target); err != nil || batch.id != "" {
		t.Fatal("claim bypassed hold", err)
	}
	// A new notifier preserves the durable gate across restarts.
	f.n = newNotifier(f.service.Store, f.n.adapter)
	if f.drain(t, 4).Submitted != 0 {
		t.Fatal("new dispatcher forgot hold")
	}
}

func TestNotificationsHoldCooldown(t *testing.T) {
	f := noticeTestFixture(t)
	clock := time.Now()
	f.n.clock = func() time.Time { return clock }
	f.send(t, f.a.Token, "First @03-worker")
	f.drain(t, 4)
	f.send(t, f.a.Token, "Later @03-worker")
	clock = clock.Add(10*time.Minute - time.Millisecond)
	if f.drain(t, 4).Submitted != 0 {
		t.Fatal("cooldown ended early")
	}
	clock = clock.Add(time.Millisecond)
	if f.drain(t, 4).Submitted != 1 || f.prompts(t) != 2 {
		t.Fatal("cooldown did not re-arm")
	}
}

func TestNotificationsHoldObservedBusy(t *testing.T) {
	for _, release := range []string{"idle", "cooldown", "unknown"} {
		t.Run(release, func(t *testing.T) {
			f := noticeTestFixture(t)
			clock := time.Now()
			f.n.clock = func() time.Time { return clock }
			if release != "unknown" {
				_, err := f.service.Store.ObserveAgent(ctx, f.b.Token, AgentObserveRequest{Context: f.ref(0), AgentID: f.bID, ExpectedRevision: 2, RuntimeID: f.config.Binding.InstanceID, State: "working"})
				if err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 20; i++ {
				f.send(t, f.a.Token, "Busy @03-worker")
				f.drain(t, 4)
			}
			if release == "unknown" {
				if f.prompts(t) != 1 {
					t.Fatal("unknown observation delayed initial notice")
				}
				return
			}
			if f.prompts(t) != 0 {
				t.Fatal("observed busy runtime received notice")
			}
			if release == "idle" {
				_, err := f.service.Store.ObserveAgent(ctx, f.b.Token, AgentObserveRequest{Context: f.ref(0), AgentID: f.bID, ExpectedRevision: 2, RuntimeID: f.config.Binding.InstanceID, State: "idle"})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				clock = clock.Add(10 * time.Minute)
			}
			if f.drain(t, 4).Submitted != 1 {
				t.Fatal("busy hold did not release")
			}
		})
	}
}
