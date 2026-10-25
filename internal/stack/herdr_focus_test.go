package stack

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHerdrFocusVerifiesBindingFirst(t *testing.T) {
	f := noticeTestFixture(t)
	focus := func(token, agent string) (map[string]string, error) {
		return f.service.herdrFocus(ctx, token, HerdrFocusRequest{Context: f.ref(0), Agent: agent})
	}
	_, err := focus(f.operator.Token, "03-worker")
	requireCode(t, err, "integration_unavailable") // the service has no --herdr-executable
	f.service.notifier = f.n                       // not running; Close must not stop it
	t.Cleanup(func() { f.service.notifier = nil })
	for _, agent := range []string{"03-worker", f.bID} {
		if data, err := focus(f.operator.Token, agent); err != nil || data["focused"] != "agent" {
			t.Fatal("focus by name or ID", agent, data, err)
		}
	}
	calls := f.calls(t)
	if last := calls[len(calls)-1].Args; strings.Join(last, " ") != "agent focus "+f.config.Binding.PaneID {
		t.Fatal("focus target", last)
	}
	_, err = focus(f.b.Token, "03-worker")
	requireCode(t, err, "forbidden")
	_, err = focus(f.operator.Token, "nobody")
	requireCode(t, err, "not_found")
	_, err = focus(f.operator.Token, "02-worker")
	requireCode(t, err, "integration_unavailable") // unbound
	// A blocked or unverified harness can be focused, a changed pane cannot.
	f.config.Status = "blocked"
	f.n.adapter.verified = func(RuntimeBinding) bool { return false }
	f.write(t)
	if data, err := focus(f.operator.Token, "03-worker"); err != nil || data["focused"] != "agent" {
		t.Fatal("blocked focus", err)
	}
	f.config.Mode = "focus_fails"
	f.write(t)
	if data, err := focus(f.operator.Token, "03-worker"); err != nil || data["focused"] != "tab" {
		t.Fatal("tab fallback", data, err)
	}
	before := len(f.calls(t))
	f.config.Mode = "pane_changed"
	f.write(t)
	_, err = focus(f.operator.Token, "03-worker")
	requireCode(t, err, "integration_unavailable")
	for _, call := range f.calls(t)[before:] {
		if call.Args[1] == "focus" {
			t.Fatal("focused a changed pane")
		}
	}
	// Over HTTP the answer names only how it focused, never the binding.
	f.config.Mode = ""
	f.write(t)
	data, err := clientForCredential(t, f.taskTeam, f.operator).Do(ctx, "POST", "/v1/herdr/focus", HerdrFocusRequest{Context: f.ref(0), Agent: "03-worker"})
	var result struct {
		Data map[string]string `json:"data"`
	}
	if err != nil || json.Unmarshal(data, &result) != nil || len(result.Data) != 1 || result.Data["focused"] != "agent" {
		t.Fatal("HTTP focus", string(data), err)
	}
	_, err = clientForCredential(t, f.taskTeam, f.operator).Do(ctx, "POST", "/v1/herdr/focus", HerdrFocusRequest{Context: f.ref(0)})
	requireCode(t, err, "invalid_input")
}
