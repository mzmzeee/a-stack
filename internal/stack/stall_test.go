package stack

import (
	"encoding/json"
	"testing"
	"time"
)

func TestUnreadMentionStallsUntilOwnRead(t *testing.T) {
	f := mailFixture(t)
	f.send(t, f.a.Token, "Question @03-worker")
	stamp := time.Now().Add(-31 * time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err := f.service.Store.db.Exec("UPDATE mail_messages SET received_at=? WHERE task_id=?", stamp, f.taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Store.db.Exec("UPDATE task_assignees SET added_at=? WHERE task_id=?", stamp, f.taskID); err != nil {
		t.Fatal(err)
	}
	check := func(want bool) {
		t.Helper()
		detail, err := f.service.Store.ShowTask(ctx, f.a.Token, TaskReadRequest{Context: f.ref(0), TaskID: f.taskID})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(detail)
		var got struct {
			MailWaits []struct {
				Stalled bool `json:"stalled"`
			} `json:"mail_waits"`
		}
		if err = json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		if (len(got.MailWaits) > 0 && got.MailWaits[0].Stalled) != want {
			t.Fatalf("stalled=%v: %s", want, b)
		}
	}
	check(true)
	browser := issueTest(t, f.service.Store, BrowserCapability, opaqueID(), f.ref(0).SessionID)
	f.read(t, browser.Token, "", 50)
	f.read(t, f.a.Token, "", 50)
	check(true)
	f.read(t, f.b.Token, "", 50)
	check(false)
}

func TestUnreadMentionOneExtraNotice(t *testing.T) {
	f := noticeTestFixture(t)
	clock := time.Now()
	f.n.clock = func() time.Time { return clock }
	f.send(t, f.a.Token, "Question @03-worker")
	f.drain(t, 4)
	clock = clock.Add(10*time.Minute + time.Second)
	if got := f.drain(t, 4).Submitted; got != 1 {
		t.Fatalf("extra notices=%d, want 1", got)
	}
	f.n = newNotifier(f.service.Store, f.n.adapter)
	f.n.clock = func() time.Time { return clock }
	for i := 0; i < 4; i++ {
		clock = clock.Add(11 * time.Minute)
		f.drain(t, 4)
	}
	if got := f.prompts(t); got != 2 {
		t.Fatalf("notices=%d, want exactly 2", got)
	}
}

func TestOrchestratorUnreachableWithoutMailRead(t *testing.T) {
	f := mailFixture(t)
	if _, err := f.service.Store.db.Exec("UPDATE agents SET binding=NULL,runtime_id=NULL,observed_at='' WHERE id=?", f.aID); err != nil {
		t.Fatal(err)
	}
	session, err := f.service.Store.InspectSession(ctx, f.a.Token, f.ref(0))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(session)
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	if got["orchestrator_warning"] != "the Orchestrator cannot be reached by mail" {
		t.Fatalf("missing warning: %s", b)
	}
}
