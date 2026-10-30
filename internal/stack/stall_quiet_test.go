package stack

import (
	"encoding/json"
	"testing"
	"time"
)

func TestUnreadExtraRespectsHookMode(t *testing.T) {
	f := noticeTestFixture(t)
	clock := time.Now().UTC()
	fixedClock.Store(&clock)
	t.Cleanup(func() { fixedClock.Store(nil) })
	f.n.clock = func() time.Time { return clock }
	f.send(t, f.a.Token, "Question @03-worker")
	f.drain(t, 4)
	binding := f.config.Binding
	binding.Notices = "hook"

	encoded, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}

	execTest(t, f.service.Store, "UPDATE agents SET binding=? WHERE id=?", string(encoded), f.bID)
	clock = clock.Add(31 * time.Minute)

	if got := f.drain(t, 4); got.Checked != 0 || f.prompts(t) != 1 {
		t.Fatal("extra notice entered hook-mode pane", got)
	}

	detail, err := f.service.Store.ShowTask(ctx, f.a.Token, TaskReadRequest{Context: f.ref(0), TaskID: f.taskID})
	if err != nil || len(detail.MailWaits) != 1 || !detail.MailWaits[0].Stalled {
		t.Fatal("hook mode hid Stalled", detail, err)
	}
}

func TestUnreadExtraRespectsStrictCooldown(t *testing.T) {
	f := noticeTestFixture(t)
	clock := time.Now().UTC()
	f.n.clock = func() time.Time { return clock }
	f.send(t, f.a.Token, "Question @03-worker")
	f.drain(t, 4)
	clock = clock.Add(11 * time.Minute)

	target, err := f.n.nextTarget(ctx, f.ref(0).SessionID)
	if err != nil {
		t.Fatal(err)
	}

	// A newer delivery cooldown must win even when the old mention is eligible.
	due := clock.Add(noticeCooldown)
	execTest(t, f.service.Store, "UPDATE notice_targets SET next_attempt=? WHERE recipient_id=?", due.UnixMilli(), f.bID)

	if batch, err := f.n.claim(ctx, target); err != nil || batch.id != "" {
		t.Fatal("claim bypassed strict cooldown", batch, err)
	}

	if got := f.drain(t, 4); got.Checked != 0 || f.prompts(t) != 1 {
		t.Fatal("selector bypassed strict cooldown", got)
	}

	clock = due

	if got := f.drain(t, 4); got.Submitted != 1 || f.prompts(t) != 2 {
		t.Fatal("extra notice did not resume at cooldown boundary", got)
	}
}

func TestHookOnlyOrchestratorNeedsRecentEvidence(t *testing.T) {
	f := noticeTestFixture(t)
	f.appoint(t, 0, f.bID, 2)

	f.service.Store.noticeWake = f.n.signal
	for _, mode := range []string{"hook", "", "pane"} {
		binding := f.config.Binding
		binding.Notices = mode

		encoded, err := json.Marshal(binding)
		if err != nil {
			t.Fatal(err)
		}

		execTest(t, f.service.Store, "UPDATE agents SET binding=? WHERE id=?", string(encoded), f.bID)

		session, err := f.service.Store.InspectSession(ctx, f.b.Token, f.ref(0))
		if err != nil || (session.OrchestratorWarning == "") != (mode == "pane") {
			t.Fatal(mode, session, err)
		}
	}
}
