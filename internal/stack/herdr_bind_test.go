package stack

import (
	"os"
	"path/filepath"
	"testing"
)

// bindFixture runs HerdrBind against the strict fake Herdr from inside the
// fixture's pane, with a fake harness that prints its version.
func bindFixture(t *testing.T, harnessOutput string) (*noticeFixture, func(string) string) {
	t.Helper()
	f := noticeTestFixture(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\necho '"+harnessOutput+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	env := map[string]string{"HERDR_ENV": "1", "HERDR_PANE_ID": f.config.Binding.PaneID, "HERDR_SOCKET_PATH": f.config.Binding.Endpoint}
	return f, func(name string) string { return env[name] }
}

func TestHerdrBindReadsOwnPane(t *testing.T) {
	f, env := bindFixture(t, "codex-cli 0.164.2")
	binding, err := HerdrBind(ctx, f.n.adapter.executable, "03-Worker", env)
	if err != nil {
		t.Fatal(err)
	}
	want := f.config.Binding
	want.InstanceID, want.HarnessVersion = "herdr:"+want.TerminalID, "0.164.2"
	if *binding != want {
		t.Fatalf("binding %+v, want %+v", *binding, want)
	}
	// The named pane is not renamed, and the binding passes the notice check.
	for _, call := range f.calls(t) {
		if len(call.Args) > 1 && call.Args[1] == "rename" {
			t.Fatal("renamed an already named pane")
		}
	}
	f.n.adapter.verified = verifiedHarness
	if reason := f.n.adapter.verify(ctx, *binding); reason != "" {
		t.Fatal("bound pane fails the notice check", reason)
	}
}

func TestHerdrBindNamesUnnamedPane(t *testing.T) {
	f, env := bindFixture(t, "codex-cli 0.164.0")
	f.config.Mode = "unnamed"
	f.write(t)
	binding, err := HerdrBind(ctx, f.n.adapter.executable, "03-Worker_With-A-Very-Long-Name-Indeed", env)
	if err != nil {
		t.Fatal(err)
	}
	if binding.HerdrAlias != "a-03-worker_with-a-very-long-nam" || !herdrName.MatchString(binding.HerdrAlias) {
		t.Fatal("alias", binding.HerdrAlias)
	}
	calls := f.calls(t)
	last := calls[len(calls)-1].Args
	if len(last) != 4 || last[1] != "rename" || last[2] != f.config.Binding.PaneID {
		t.Fatal("rename did not target the caller's own pane", last)
	}
	f.config.Mode = "rename_taken"
	f.write(t)
	_, err = HerdrBind(ctx, f.n.adapter.executable, "03-worker", env)
	requireCode(t, err, "conflict")
}

func TestHerdrBindRefusals(t *testing.T) {
	for _, test := range []struct{ mode, harness, code string }{
		{"missing", "codex-cli 0.164.0", "integration_missing"},
		{"kind_changed", "codex-cli 0.164.0", "integration_missing"},
		{"version", "codex-cli 0.164.0", "unsupported_herdr"},
		{"not_running", "codex-cli 0.164.0", "herdr_unavailable"},
		{"malformed", "codex-cli 0.164.0", "integration_missing"},
		{"", "no version here", "integration_unavailable"},
		{"outside", "codex-cli 0.164.0", "invalid_input"},
	} {
		t.Run(test.mode+test.harness, func(t *testing.T) {
			f, env := bindFixture(t, test.harness)
			f.config.Mode = test.mode
			f.write(t)
			if test.mode == "outside" {
				env = func(string) string { return "" }
			}
			binding, err := HerdrBind(ctx, f.n.adapter.executable, "03-worker", env)
			code := test.code
			if code == "unsupported_herdr" || code == "herdr_unavailable" {
				code = "integration_unavailable"
			}
			requireCode(t, err, code)
			if binding != nil {
				t.Fatal("refused bind returned a binding")
			}
			if test.code == "integration_missing" && test.mode == "missing" {
				if e := err.(*Error); e.Message != "Herdr has no session for this pane; run herdr integration install codex, restart codex in this pane, then bind again" {
					t.Fatal("hint", e.Message)
				}
			}
			for _, call := range f.calls(t) {
				if len(call.Args) > 1 && (call.Args[1] == "rename" || call.Args[1] == "prompt" || call.Args[0] == "pane") {
					t.Fatal("refused bind changed Herdr", call.Args)
				}
			}
		})
	}
}
