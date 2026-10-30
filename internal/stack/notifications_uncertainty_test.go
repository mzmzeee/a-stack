package stack

import (
	"encoding/json"
	"testing"
)

func unresolvedCount(t *testing.T, value any) int {
	t.Helper()

	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}

	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}

	count, _ := fields["unresolved_uncertain"].(float64)

	return int(count)
}

func TestUnresolvedNoticesSurviveLaterSuccess(t *testing.T) {
	for _, reason := range []string{"delivery_uncertain", "recovery_uncertain"} {
		t.Run(reason, func(t *testing.T) {
			f := noticeTestFixture(t)
			f.service.Store.noticeWake = f.n.signal
			f.send(t, f.a.Token, "First @03-worker")

			before := countTest(t, f.service.Store, "SELECT read_revision FROM sessions WHERE id='"+f.sessions[0]+"'")
			if reason == "delivery_uncertain" {
				f.config.Mode = "bad_success"
				f.write(t)

				if f.drain(t, 1).Uncertain != 1 {
					t.Fatal("missing uncertain outcome")
				}
			} else {
				target, err := f.n.nextTarget(ctx, f.sessions[0])
				if err != nil {
					t.Fatal(err)
				}

				if _, err = f.n.claim(ctx, target); err != nil {
					t.Fatal(err)
				}

				if err = f.service.Store.recoverNotices(ctx); err != nil {
					t.Fatal(err)
				}
			}

			if after := countTest(t, f.service.Store, "SELECT read_revision FROM sessions WHERE id='"+f.sessions[0]+"'"); after <= before {
				t.Fatal("uncertainty did not refresh the Agents snapshot")
			}

			checkWarning := func(wantReason string) {
				t.Helper()

				check, err := f.service.Store.CheckMail(ctx, f.b.Token, MailCheckRequest{Context: f.ref(0)})
				if err != nil || check.NoticeWarning == nil || unresolvedCount(t, check.NoticeWarning) != 1 || check.NoticeWarning.Reason != wantReason {
					t.Fatal("own mail hid uncertainty", check.NoticeWarning, err)
				}

				browser := issueTest(t, f.service.Store, BrowserCapability, "uncertain-view", f.sessions[0])
				page, err := f.service.Store.SnapshotPage(ctx, browser.Token, SnapshotPageRequest{Context: f.ref(0), Kind: "agents"})
				if err != nil {
					t.Fatal(err)
				}

				found := false
				for _, item := range page.Items {
					agent := item.(SnapshotAgent)
					if agent.ID == f.bID {
						found = true

						if agent.NoticeWarning == nil || unresolvedCount(t, agent.NoticeWarning) != 1 {
							t.Fatal("Agents panel hid uncertainty", agent.NoticeWarning)
						}
					}
				}

				if !found {
					t.Fatal("missing Agent")
				}

				status, err := f.service.notificationStatus(ctx, f.b.Token, NoticeRequest{Context: f.ref(0)})
				if err != nil || len(status.Targets) != 1 || unresolvedCount(t, status.Targets[0]) != 1 || status.Targets[0].Reason != wantReason {
					t.Fatal("status hid uncertainty", status, err)
				}
			}
			// Status uses the stored outcomes even with its adapter disabled.
			checkWarning(reason)
			f.config.Mode = ""
			f.write(t)
			f.send(t, f.a.Token, "Later @03-worker")

			if f.drain(t, 1).Submitted != 1 {
				t.Fatal("later submission failed")
			}

			checkWarning("delivery_uncertain")

			if f.drain(t, 4).Checked != 0 {
				t.Fatal("uncertainty was resubmitted")
			}
		})
	}
}
