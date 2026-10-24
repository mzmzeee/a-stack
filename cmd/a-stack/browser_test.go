package main

import (
	"bytes"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
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
}
