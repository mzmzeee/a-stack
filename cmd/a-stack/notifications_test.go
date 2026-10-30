package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestCLINotificationsDisabledStatusAndBounds(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	launch(t, dir)
	common := func(args ...string) []string { return append(args, "--state-dir", dir, "--json") }
	w := receiptResult(t, common("workspace", "create", "--operation-id", "notice-workspace", "--path", t.TempDir()))
	s := receiptResult(t, common("session", "create", "--operation-id", "notice-session", "--workspace", w.ResourceID, "--name", "Notices"))
	var out, diagnostic bytes.Buffer

	code := run(common("notifications", "status", "--workspace", w.ResourceID, "--session", s.ResourceID), &out, &diagnostic)
	var response struct {
		Data stack.NoticeStatus `json:"data"`
	}
	if code != 0 || diagnostic.Len() != 0 || json.Unmarshal(out.Bytes(), &response) != nil || response.Data.Enabled || len(response.Data.Targets) != 0 {
		t.Fatal("disabled notification status")
	}

	for _, args := range [][]string{{"notifications", "drain", "--workspace", w.ResourceID, "--session", s.ResourceID}, {"notifications", "status", "--workspace", w.ResourceID}, {"notifications", "drain", "--workspace", w.ResourceID, "--session", s.ResourceID, "--limit", "17"}} {
		out.Reset()
		diagnostic.Reset()

		if run(common(args...), &out, &diagnostic) == 0 || out.Len() != 0 {
			t.Fatal("invalid/disabled drain succeeded")
		}
	}

	out.Reset()
	diagnostic.Reset()

	if run([]string{"--help", "--json"}, &out, &diagnostic) != 0 || !strings.Contains(out.String(), "notifications status") || !strings.Contains(out.String(), "--herdr-executable") {
		t.Fatal("notification help absent")
	}
}
