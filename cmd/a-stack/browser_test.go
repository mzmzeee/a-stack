package main

import (
	"bytes"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestOpenPrintsScopedURLWhenLaunchFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	launch(t, dir)
	common := func(args ...string) []string { return append(args, "--state-dir", dir, "--json") }
	w := receiptResult(t, common("workspace", "create", "--operation-id", "browser-workspace", "--path", t.TempDir()))
	s := receiptResult(t, common("session", "create", "--operation-id", "browser-session", "--workspace", w.ResourceID, "--name", "Read only"))
	old := launchBrowser
	t.Cleanup(func() { launchBrowser = old })
	var launched string
	launchBrowser = func(u string) error {
		launched = u
		return errors.New("simulated opener failure containing private input")
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"open", "--state-dir", dir, "--workspace", w.ResourceID, "--session", s.ResourceID}, &stdout, &stderr)
	u, err := url.Parse(strings.TrimSpace(stdout.String()))
	if code != 0 || err != nil || u.Scheme != "http" || len(u.Fragment) != 43 || u.RawQuery != "" || launched != strings.TrimSpace(stdout.String()) || !strings.Contains(stderr.String(), "printed URL") || strings.Contains(stderr.String(), u.Fragment) {
		t.Fatal("open did not preserve usable URL safely")
	}
	stdout.Reset()
	stderr.Reset()
	launched = ""
	code = run([]string{"open", "--state-dir", dir, "--workspace", w.ResourceID, "--session", s.ResourceID, "--no-browser", "--json"}, &stdout, &stderr)
	if code != 0 || launched != "" || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"url"`) {
		t.Fatal("no-browser JSON")
	}
	// Only the Operator opens views and revokes them.
	private := filepath.Join(t.TempDir(), "credentials")
	if stack.MkdirPrivate(private) != nil {
		t.Fatal("private credential dir")
	}
	agentFile := filepath.Join(private, "agent.json")
	ref := []string{"--workspace", w.ResourceID, "--session", s.ResourceID}
	receiptResult(t, common(append([]string{"agent", "register", "--name", "01-orchestrator", "--operation-id", "browser-agent", "--new-credential-file", agentFile}, ref...)...))
	asAgent := func(args ...string) []string {
		return common(append(append(args, ref...), "--as", "agent", "--credential-file", agentFile)...)
	}
	for _, args := range [][]string{asAgent("open", "--no-browser"), asAgent("view", "revoke")} {
		if _, errout := commandResult(t, args, 1); !strings.Contains(string(errout), `"forbidden"`) {
			t.Fatalf("%v: %s", args[:2], errout)
		}
	}
	for _, args := range [][]string{common("view", "revoke"), common(append([]string{"view", "revoke", "--all"}, ref...)...)} {
		commandResult(t, args, 2)
	}
	out, _ := commandResult(t, common(append([]string{"view", "revoke"}, ref...)...), 0)
	if strings.TrimSpace(string(out)) != `{"data":{"revoked":0}}` {
		t.Fatalf("view revoke: %s", out)
	}
	out, _ = commandResult(t, common("view", "revoke", "--all"), 0)
	if strings.TrimSpace(string(out)) != `{"data":{"revoked":0}}` {
		t.Fatalf("view revoke --all: %s", out)
	}
}
