package stack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
)

func TestSharedHookBindingWithoutActorNeverProbesServer(t *testing.T) {
	for _, adapter := range []bool{false, true} {
		for _, login := range []string{"", "invalid\x01login", "friend@example.com"} {
			t.Run(fmt.Sprintf("adapter=%t/login=%q", adapter, login), func(t *testing.T) {
				f := noticeTestFixture(t)
				s := f.service
				a, c := lifecycleReviewAgent(t, s.Store, f.operator, f.ref(0), "tagged-client")
				b := f.config.Binding
				b.InstanceID = "herdr:" + b.TerminalID
				b.Endpoint = "/remote-only/laptop.sock"
				b.NativeSessionID = "tagged-native"
				b.Notices = "hook"
				f.config.Binding = b
				f.write(t)

				if err := s.SetServeHost("relay.example.ts.net"); err != nil {
					t.Fatal(err)
				}

				if adapter {
					s.notifier = f.n
					defer func() { s.notifier = nil }()
				}

				send := func(revision int64) *httptest.ResponseRecorder {
					q := AgentBindingRequest{Context: f.ref(0), OperationID: opaqueID(), AgentID: a.ResourceID, ExpectedRevision: revision, Runtime: &b}
					raw, _ := json.Marshal(q)
					r := httptest.NewRequest("POST", "https://relay.example.ts.net/v1/herdr/bind", bytes.NewReader(raw))
					r.RemoteAddr = "127.0.0.1:45678"
					r.Header.Set("Authorization", "Bearer "+c.Token)
					r.Header.Set("X-A-Stack-Instance", s.Discovery.InstanceID)
					r.Header.Set("Content-Type", "application/json")

					if login != "" {
						r.Header.Set("Tailscale-User-Login", login)
					}

					w := httptest.NewRecorder()
					s.dispatch(w, r)

					return w
				}

				w := send(1)
				if w.Code != 200 {
					t.Fatalf("fresh shared attach HTTP %d", w.Code)
				}

				var remote bool
				if err := s.Store.db.QueryRow("SELECT json_extract(binding,'$.remote_machine') FROM agents WHERE id=?", a.ResourceID).Scan(&remote); err != nil || !remote {
					t.Error("shared ingress not stored as remote", err)
				}

				if w = send(2); w.Code != 409 {
					t.Errorf("remote replacement must conflict: %d", w.Code)
				}

				_, err := s.herdrFocus(ctx, f.operator.Token, HerdrFocusRequest{Context: f.ref(0), AgentID: a.ResourceID})
				requireCode(t, err, "integration_unavailable")

				if n := len(f.calls(t)); n != 0 {
					t.Fatalf("remote hook bind/focus invoked server Herdr %d times", n)
				}
			})
		}
	}
}
