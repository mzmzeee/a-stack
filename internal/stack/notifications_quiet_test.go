package stack

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNoticeModes(t *testing.T) {
	for _, test := range []struct {
		name, mode                  string
		orchestrator, legacy, typed bool
	}{
		{"worker-default", "", false, false, true},
		{"worker-hook", "hook", false, false, false},
		{"orchestrator-default", "", true, false, false},
		{"orchestrator-pane-opt-in", "pane", true, false, true},
		{"legacy-orchestrator", "", true, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := noticeTestFixture(t)
			if test.orchestrator {
				f.appoint(t, 0, f.bID, 2)
			}
			binding := f.config.Binding
			binding.Notices = test.mode
			credential := opaqueID()
			_, err := f.service.Store.BindAgent(ctx, f.operator.Token, "rebind", AgentBindingRequest{Context: f.ref(0), OperationID: opaqueID(), AgentID: f.bID, ExpectedRevision: 2, Runtime: &binding, CredentialHash: tokenHash(credential)})
			if err != nil {
				t.Fatal(err)
			}
			if test.legacy {
				encoded, _ := json.Marshal(binding)
				execTest(t, f.service.Store, "UPDATE agents SET binding=? WHERE id=?", string(encoded), f.bID)
			}
			for range 7 {
				f.send(t, f.a.Token, "Act @03-worker")
				f.drain(t, 4)
				clock := f.n.clock().Add(noticeCooldown)
				f.n.clock = func() time.Time { return clock }
			}
			want := 0
			if test.typed {
				want = 7
			}
			if f.prompts(t) != want {
				t.Fatalf("typed %d notices, want %d", f.prompts(t), want)
			}
			if !test.typed {
				check, err := f.service.Store.CheckMail(ctx, credential, MailCheckRequest{Context: f.ref(0)})
				if err != nil || check.NoticeWarning != nil {
					t.Fatalf("hook-only mail incorrectly asks for pane setup: %v %v", check.NoticeWarning, err)
				}
			}
		})
	}
	if err := validateBinding(&RuntimeBinding{InstanceID: "valid", Notices: "invalid"}, true); err == nil {
		t.Fatal("invalid mode accepted")
	}
}
