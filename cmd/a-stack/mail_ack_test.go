package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// This uses only the CLI and wire fields, so it also runs against schema 11.
func TestMailAcknowledgementCLI(t *testing.T) {
	f := newTeam(t)
	args := f.scoped("mail", "send", "--task", f.task, "--body", "Confirm this handoff", "--ack-required", "--ack-from", "02-worker", "--ack-from", "02-worker", "--ack-due", "1h", "--operation-id", "ack-cli-send")

	out, stderr, code := f.run(f.workerFile, args...)
	if code != 0 {
		t.Fatalf("ack request: %d %s %s", code, out, stderr)
	}

	var receipt struct {
		Data struct {
			ID string `json:"resource_id"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(out), &receipt) != nil || receipt.Data.ID == "" {
		t.Fatal(out)
	}

	for _, args := range [][]string{f.scoped("mail", "check"), f.scoped("task", "handoff", "--task", f.task, "--json")} {
		out, stderr, code = f.run(f.workerFile, args...)
		if code != 0 || !strings.Contains(out, "pending_acks") || !strings.Contains(out, receipt.Data.ID) {
			t.Fatalf("pending: %d %s %s", code, out, stderr)
		}
	}

	out, stderr, code = f.run(f.workerFile, f.scoped("task", "handoff", "--task", f.task)...)
	if code != 0 || !strings.Contains(out, "Pending acknowledgements (informational)") {
		t.Fatalf("handoff: %d %s %s", code, out, stderr)
	}

	ack := f.scoped("mail", "ack", "--message", receipt.Data.ID, "--operation-id", "ack-cli-explicit")

	out, stderr, code = f.run(f.workerFile, ack...)
	if code != 0 || !strings.Contains(out, "acknowledged") {
		t.Fatalf("ack: %d %s %s", code, out, stderr)
	}

	again, stderr, code := f.run(f.workerFile, ack...)
	if code != 0 || again != out {
		t.Fatalf("ack retry: %d %s", code, stderr)
	}

	out, stderr, code = f.run(f.workerFile, f.scoped("mail", "check")...)
	if code != 0 || strings.Contains(out, "pending_acks") {
		t.Fatalf("ack not cleared: %d %s %s", code, out, stderr)
	}

	out, stderr, code = f.run(f.workerFile, f.scoped("mail", "read", "--task", f.task)...)
	if code != 0 || !strings.Contains(out, "acked_at") || !strings.Contains(out, "recipient_id") {
		t.Fatalf("missing retained actor/time: %d %s %s", code, out, stderr)
	}
}
