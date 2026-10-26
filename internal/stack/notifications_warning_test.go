package stack

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNoticeWarningsOnOwnMailAndBrowserAgents(t *testing.T) {
	for _, reason := range []string{"binding_incomplete", "runtime_unavailable", "unsupported_harness", "identity_changed", "integration_disabled", "submitted"} {
		t.Run(reason, func(t *testing.T) {
			f := noticeTestFixture(t)
			s := f.service.Store
			if reason != "integration_disabled" {
				s.noticeWake = f.n.signal
			}
			switch reason {
			case "integration_disabled":
			case "binding_incomplete":
				if _, err := s.db.Exec("UPDATE agents SET binding=NULL WHERE id=?", f.bID); err != nil {
					t.Fatal(err)
				}
			case "unsupported_harness":
				binding := f.config.Binding
				binding.HarnessVersion = "0.165.0"
				encoded, _ := json.Marshal(binding)
				if _, err := s.db.Exec("UPDATE agents SET binding=? WHERE id=?", string(encoded), f.bID); err != nil {
					t.Fatal(err)
				}
			default:
				if err := f.n.unavailable(ctx, noticeTarget{agent: f.bID, session: f.sessions[0], revision: 2, bindingJSON: string(mustJSON(t, f.config.Binding))}, reason); err != nil {
					t.Fatal(err)
				}
			}
			before := countTest(t, s, "SELECT read_revision FROM sessions WHERE id='"+f.sessions[0]+"'")
			check, err := s.CheckMail(ctx, f.b.Token, MailCheckRequest{Context: f.ref(0)})
			if err != nil {
				t.Fatal(err)
			}
			browser := issueTest(t, s, BrowserCapability, "notice-view", f.sessions[0])
			page, err := s.SnapshotPage(ctx, browser.Token, SnapshotPageRequest{Context: f.ref(0), Kind: "agents"})
			if err != nil {
				t.Fatal(err)
			}
			var viewed *NoticeWarning
			for _, item := range page.Items {
				a := item.(SnapshotAgent)
				if a.ID == f.bID {
					viewed = a.NoticeWarning
				}
			}
			for _, warning := range []*NoticeWarning{check.NoticeWarning, viewed} {
				if reason == "submitted" {
					if warning != nil {
						t.Fatal("healthy target got a warning", warning)
					}
				} else if warning == nil || warning.Reason != reason || warning.Fix == "" {
					t.Fatal("missing bounded warning", warning)
				}
			}
			encoded, _ := json.Marshal(page)
			for _, secret := range []string{f.config.Binding.Endpoint, f.config.Binding.NativeSessionID, f.config.Binding.TerminalID, f.b.Token} {
				if strings.Contains(string(encoded), secret) {
					t.Fatal("private identity leaked")
				}
			}
			if after := countTest(t, s, "SELECT read_revision FROM sessions WHERE id='"+f.sessions[0]+"'"); before != after {
				t.Fatal("warning read wrote state")
			}
			if len(f.calls(t)) != 0 {
				t.Fatal("reading warnings ran Herdr")
			}
		})
	}
}

func TestNoticeDiagnosticChangesRefreshViewAndStatusNames(t *testing.T) {
	f := noticeTestFixture(t)
	f.service.Store.noticeWake = f.n.signal
	f.config.Status = "blocked"
	f.write(t)
	f.send(t, f.a.Token, "Hello @03-worker")
	before := countTest(t, f.service.Store, "SELECT read_revision FROM sessions WHERE id='"+f.sessions[0]+"'")
	f.drain(t, 4)
	after := countTest(t, f.service.Store, "SELECT read_revision FROM sessions WHERE id='"+f.sessions[0]+"'")
	if after <= before {
		t.Fatal("the view will not refresh its notice marker")
	}
	f.n.clock = func() time.Time { return time.Now().Add(time.Hour) }
	f.drain(t, 4)
	if got := countTest(t, f.service.Store, "SELECT read_revision FROM sessions WHERE id='"+f.sessions[0]+"'"); got != after {
		t.Fatal("same diagnostic churned the read revision")
	}
	f.config.Status = "working"
	f.write(t)
	f.n.clock = func() time.Time { return time.Now().Add(2 * time.Hour) }
	f.drain(t, 4)
	check, err := f.service.Store.CheckMail(ctx, f.b.Token, MailCheckRequest{Context: f.ref(0)})
	if err != nil || check.NoticeWarning != nil {
		t.Fatal("successful retry kept the warning", check.NoticeWarning, err)
	}
	status, err := f.service.notificationStatus(ctx, f.operator.Token, NoticeRequest{Context: f.ref(0)})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range status.Targets {
		if target.AgentID == "" || target.AgentName == "" {
			t.Fatal("unnamed notice target", target)
		}
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
