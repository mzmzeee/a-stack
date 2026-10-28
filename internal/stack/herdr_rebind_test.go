package stack

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func replacementBinding(old RuntimeBinding) RuntimeBinding {
	next := old
	next.PaneID, next.TerminalID, next.NativeSessionID, next.HerdrAlias = "w1:p9", "terminal-9", "native-9", "replacement-b"
	next.InstanceID = "herdr:" + next.TerminalID
	return next
}

func TestHerdrBindReplacesGonePaneAndKeepsNoticeStates(t *testing.T) {
	f, _ := bindFixture(t, "codex-cli 0.164.0")
	old := f.config.Binding
	next := replacementBinding(old)
	f.config.Binding, f.config.MissingPane = next, old.PaneID
	f.write(t)
	f.service.Store.noticeWake = f.n.signal
	if err := f.n.unavailable(ctx, noticeTarget{agent: f.bID, session: f.sessions[0], revision: 2, bindingJSON: string(mustJSON(t, old))}, "runtime_unavailable"); err != nil {
		t.Fatal(err)
	}
	binding, err := HerdrBind(ctx, f.n.adapter.executable, "03-worker", func(name string) string {
		return map[string]string{"HERDR_ENV": "1", "HERDR_PANE_ID": next.PaneID, "HERDR_SOCKET_PATH": next.Endpoint}[name]
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"uncertain", "suppressed", "pending"} {
		msg := f.send(t, f.a.Token, "Old target @03-worker")
		if _, err := f.service.Store.db.Exec("UPDATE mail_mentions SET notice_state=? WHERE message_id=?", state, msg.ResourceID); err != nil {
			t.Fatal(err)
		}
	}
	r := AgentBindingRequest{Context: f.ref(0), AgentID: f.bID, ExpectedRevision: 2, OperationID: opaqueID(), Runtime: binding}
	// Generic attach must not gain permission to replace a binding.
	_, err = f.service.Store.BindAgent(ctx, f.b.Token, "attach", r)
	requireCode(t, err, "conflict")
	receipt, err := f.service.Store.bindHerdrAgent(ctx, f.b.Token, r, f.n.adapter)
	if err != nil || receipt.Revision != 3 {
		t.Fatal("bind in a new pane after the old pane is gone:", receipt, err)
	}
	check, err := f.service.Store.CheckMail(ctx, f.b.Token, MailCheckRequest{Context: f.ref(0)})
	if err != nil || check.NoticeWarning == nil || check.NoticeWarning.UnresolvedUncertain != 1 {
		t.Fatal("new binding hid unresolved uncertainty", check.NoticeWarning, err)
	}
	before := len(f.calls(t))
	replay, err := f.service.Store.bindHerdrAgent(ctx, f.b.Token, r, f.n.adapter)
	if err != nil || replay != receipt || len(f.calls(t)) != before {
		t.Fatal("retry must replay without Herdr calls", replay, err)
	}
	if r := f.drain(t, 4); r.Submitted != 1 || f.prompts(t) != 1 {
		t.Fatal("pending notice did not follow the new binding", r)
	}
	for _, state := range []string{"uncertain", "suppressed", "submitted"} {
		if countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='"+state+"'") != 1 {
			t.Fatal("terminal notice state was retried or lost", state)
		}
	}
	// Old claims were released, and the credential was not rotated.
	if countTest(t, f.service.Store, "SELECT count(*) FROM runtime_claims WHERE agent_id='"+f.bID+"'") != len(concreteRuntimeKeys(binding)) {
		t.Fatal("old claims remain")
	}
	if _, err := f.service.Store.CheckMail(ctx, f.b.Token, MailCheckRequest{Context: f.ref(0)}); err != nil {
		t.Fatal("stale replacement rotated the credential", err)
	}
}

func TestHerdrBindStaleEvidenceAndGuards(t *testing.T) {
	for _, test := range []struct {
		mode, status string
		stale        bool
	}{
		{"", "working", false}, {"", "idle", false}, {"", "done", false}, {"", "blocked", false}, {"", "stopped", false},
		{"native_changed", "working", true}, {"mismatch", "working", true}, {"pane_missing", "working", true}, {"claude", "working", true},
		{"alias_reused", "working", false}, {"pane_changed", "working", false},
		{"missing", "working", false}, {"malformed", "working", false}, {"large", "working", false}, {"native_bad", "working", false},
		{"version", "working", false}, {"not_running", "working", false},
	} {
		t.Run(test.mode+test.status, func(t *testing.T) {
			f := noticeTestFixture(t)
			old := f.config.Binding
			next := replacementBinding(old)
			f.config.Binding, f.config.Targets = next, []RuntimeBinding{old}
			f.config.OldPane, f.config.OldMode, f.config.Status = old.PaneID, test.mode, test.status
			f.write(t)
			r := AgentBindingRequest{Context: f.ref(0), AgentID: f.bID, ExpectedRevision: 2, OperationID: opaqueID(), Runtime: &next}
			_, err := f.service.Store.bindHerdrAgent(ctx, f.b.Token, r, f.n.adapter)
			if test.stale {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				requireCode(t, err, "conflict")
				for _, text := range []string{"03-worker", "Operator", "agent rebind --clear-runtime", "bind again"} {
					if !strings.Contains(err.Error(), text) {
						t.Fatal("missing recovery advice", err)
					}
				}
			}
		})
	}
}

func TestHerdrBindHTTP(t *testing.T) {
	f := noticeTestFixture(t)
	old := f.config.Binding
	next := replacementBinding(old)
	f.config.Binding, f.config.MissingPane = next, old.PaneID
	f.write(t)
	r := AgentBindingRequest{Context: f.ref(0), AgentID: f.bID, ExpectedRevision: 2, OperationID: opaqueID(), Runtime: &next}
	worker := clientForCredential(t, f.taskTeam, f.b)
	_, err := worker.Do(ctx, "POST", "/v1/herdr/bind", r)
	requireCode(t, err, "conflict")
	if err := f.service.enableNotifications(f.n.adapter); err != nil {
		t.Fatal(err)
	}
	browser := issueTest(t, f.service.Store, BrowserCapability, "bind-view", r.SessionID)
	for _, credential := range []Credential{f.operator, f.a, browser} {
		client := clientForCredential(t, f.taskTeam, credential)
		_, err := client.Do(ctx, "POST", "/v1/herdr/bind", r)
		requireCode(t, err, "forbidden")
	}
	data, err := worker.Do(ctx, "POST", "/v1/herdr/bind", r)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Data Receipt `json:"data"`
	}
	if json.Unmarshal(data, &result) != nil || result.Data.Revision != 3 {
		t.Fatal("bind receipt", string(data))
	}
}

func TestHerdrBindMetadataRunsOutsideTransactionAndRechecksRevision(t *testing.T) {
	f := noticeTestFixture(t)
	old := f.config.Binding
	next := replacementBinding(old)
	f.config.Binding, f.config.MissingPane, f.config.Mode = next, old.PaneID, "get_wait"
	f.write(t)
	r := AgentBindingRequest{Context: f.ref(0), AgentID: f.bID, ExpectedRevision: 2, OperationID: opaqueID(), Runtime: &next}
	done := make(chan error, 1)
	go func() { _, err := f.service.Store.bindHerdrAgent(ctx, f.b.Token, r, f.n.adapter); done <- err }()
	f.wait(t, "get-started")
	// This must complete while Herdr is still waiting, not after its timeout.
	queryCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err := f.service.Store.db.ExecContext(queryCtx, "UPDATE agents SET revision=revision+1 WHERE id=?", f.bID); err != nil {
		t.Fatal(err)
	}
	f.release(t)
	requireCode(t, <-done, "conflict")
	if got := countTest(t, f.service.Store, "SELECT count(*) FROM runtime_claims WHERE agent_id='"+f.bID+"'"); got != len(concreteRuntimeKeys(&old)) {
		t.Fatal("stale verification changed claims after revision changed")
	}
}

func TestHerdrBindReplacementAuthorizationAndTargetChecks(t *testing.T) {
	for _, test := range []string{"other-agent", "operator", "closed", "revision", "new-target", "occupied", "partial-old", "no-adapter"} {
		t.Run(test, func(t *testing.T) {
			f := noticeTestFixture(t)
			old := f.config.Binding
			next := replacementBinding(old)
			f.config.Binding, f.config.MissingPane = next, old.PaneID
			f.write(t)
			r := AgentBindingRequest{Context: f.ref(0), AgentID: f.bID, ExpectedRevision: 2, OperationID: opaqueID(), Runtime: &next}
			token, code, adapter := f.b.Token, "conflict", f.n.adapter
			switch test {
			case "other-agent":
				token, code = f.a.Token, "forbidden"
			case "operator":
				token, code = f.operator.Token, "forbidden"
			case "closed":
				if _, err := f.service.Store.db.Exec("UPDATE sessions SET closed=1 WHERE id=?", r.SessionID); err != nil {
					t.Fatal(err)
				}
				code = "session_closed"
			case "revision":
				r.ExpectedRevision = 1
			case "new-target":
				next.NativeSessionID = "unverified-native"
				code = "integration_unavailable"
			case "occupied":
				if _, err := f.service.Store.BindAgent(ctx, f.a.Token, "attach", AgentBindingRequest{Context: f.ref(0), AgentID: f.aID, ExpectedRevision: 1, OperationID: opaqueID(), Runtime: &next}); err != nil {
					t.Fatal(err)
				}
			case "partial-old":
				encoded, _ := json.Marshal(RuntimeBinding{InstanceID: old.InstanceID})
				if _, err := f.service.Store.db.Exec("UPDATE agents SET binding=? WHERE id=?", string(encoded), f.bID); err != nil {
					t.Fatal(err)
				}
			case "no-adapter":
				adapter = nil
			}
			_, err := f.service.Store.bindHerdrAgent(ctx, token, r, adapter)
			requireCode(t, err, code)
		})
	}
}
