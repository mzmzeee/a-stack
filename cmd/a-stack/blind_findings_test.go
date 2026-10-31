package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestBlindCursorPreflight(t *testing.T) {
	f := newTeam(t)
	for _, path := range []string{filepath.Join(t.TempDir(), "missing", "mail.json"), t.TempDir()} {
		// Invalid destinations must fail before even consulting the client.
		if _, err := mailCheck(context.Background(), nil, stack.MailCheckRequest{}, path, true, func([]byte) error { t.Fatal("delivered before preflight"); return nil }); err == nil {
			t.Fatal("invalid cursor accepted")
		}

		out, stderr, code := f.run(f.workerFile, f.scoped("mail", "check", "--cursor-file", path)...)
		if code == 0 || out != "" || !strings.Contains(stderr, "cursor") || (!strings.Contains(stderr, "folder") && !strings.Contains(stderr, "directory")) {
			t.Fatalf("cursor preflight: %d %s %s", code, out, stderr)
		}
	}
}

func TestBlindWorkspaceRoots(t *testing.T) {
	f := newTeam(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	for _, path := range []string{home, filepath.Dir(home), filepath.VolumeName(home) + string(filepath.Separator)} {
		_, stderr, code := f.run("", "workspace", "create", "--path", path, "--operation-id", operationID())
		if code == 0 || !strings.Contains(stderr, "not your home folder") {
			t.Fatalf("root accepted: %d %s", code, stderr)
		}
	}
}

func TestBlindSelfMentionAckAndDisabledNotices(t *testing.T) {
	f := newTeam(t)
	f.receipt(f.workerFile, f.scoped("mail", "send", "--task", f.task, "--body", "@02-worker", "--ack-required", "--operation-id", "blind-self-ack")...)
	f.receipt(f.workerFile, f.scoped("mail", "send", "--task", f.task, "--body", "self selection", "--ack-required", "--ack-from", "02-worker", "--operation-id", "blind-self-selected-ack")...)

	out, stderr, code := f.run(f.workerFile, f.scoped("mail", "check")...)
	var page struct {
		Data stack.MailCheckPage `json:"data"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &page) != nil || len(page.Data.PendingAcks) != 0 || page.Data.NoticeWarning != nil {
		t.Fatalf("self ack / disabled notice: %s %s", out, stderr)
	}
}

func TestBlindUrgentPromptCountsNormal(t *testing.T) {
	f := newTeam(t)
	hookHome(t)
	f.taskChange(f.orchFile, "blind-assign-orch", 2, "assign", "--agent", f.orch)
	id := hookIdentity{harness: "claude", state: f.state, credential: f.workerFile, agent: f.worker, worktree: f.project, ref: stack.Context{WorkspaceID: f.workspace, SessionID: f.session}, noStopGuard: true}
	enrollTestHook(t, id, "abc", "")
	hookEvent(t, id, "SessionStart", "")
	f.receipt(f.orchFile, f.scoped("mail", "send", "--task", f.task, "--body", "normal", "--operation-id", "blind-normal")...)
	f.receipt(f.orchFile, f.scoped("mail", "send", "--task", f.task, "--body", "urgent", "--urgent", "--operation-id", "blind-urgent")...)

	out := hookEvent(t, id, "UserPromptSubmit", "")
	if !strings.Contains(out, urgentMailNudge) || !strings.Contains(out, "1 new a-stack mails") {
		t.Fatal("swallowed normal count", out)
	}

	if out = hookEvent(t, id, "UserPromptSubmit", ""); out != "" {
		t.Fatal("replayed prompt", out)
	}
}

func TestBlindHandOffPrivateParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode bits")
	}

	root := t.TempDir()
	state := filepath.Join(root, "state")
	launch(t, state)

	agents := filepath.Join(root, "a-stack-agents")
	if err := os.Mkdir(agents, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(agents, 0o777); err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())

	var out, stderr bytes.Buffer
	code := run([]string{"new", "--orchestrator", "01-orch", "--state-dir", state, "--no-browser", "--json"}, &out, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "private") {
		t.Fatalf("unsafe parent accepted: %d %s", code, stderr.String())
	}
}

func TestBlindActionableErrors(t *testing.T) {
	f := newTeam(t)
	_, stderr, code := f.run(f.workerFile, f.scoped("mail", "send", "--task", f.task, "--body", "@02-worker", "--ack-required", "--ack-due", "9999999h", "--operation-id", "blind-overflow")...)
	if code == 0 || !strings.Contains(stderr, "range") {
		t.Error("overflow:", code, stderr)
	}

	replacement := filepath.Join(filepath.Dir(f.workerFile), "replacement.json")
	f.receipt("", f.scoped("agent", "rebind", "--clear-runtime", "--agent", f.worker, "--expected-revision", "1", "--new-credential-file", replacement, "--operation-id", "blind-rebind")...)

	_, stderr, code = f.run(f.workerFile, f.scoped("mail", "check")...)
	if code == 0 || !strings.Contains(stderr, "replaced") {
		t.Error("rotated:", code, stderr)
	}

	client, err := stack.NewClient(f.state, replacement, stack.AgentCapability)
	if err != nil {
		t.Fatal(err)
	}

	client.Discovery.InstanceID = "old"
	if _, err := client.Do(context.Background(), "GET", "/v1/health", nil); err == nil || !strings.Contains(err.Error(), "copy the current service.json") {
		t.Error("instance recovery:", err)
	}

	if err := os.Remove(filepath.Join(f.state, "service.json")); err != nil {
		t.Fatal(err)
	}

	_, stderr, code = f.run("", "health")
	stderr = blindErrorMessage(t, stderr)
	if code == 0 || !strings.Contains(stderr, "--state-dir") || !strings.Contains(stderr, f.state) {
		t.Error("service hint:", code, stderr)
	}

	t.Chdir(f.project)

	_, stderr, code = f.run("", "new", "--orchestrator", "01-new", "--no-browser")
	stderr = blindErrorMessage(t, stderr)
	if code == 0 || !strings.Contains(stderr, "--state-dir") || !strings.Contains(stderr, f.state) {
		t.Error("handoff hint:", code, stderr)
	}
}

func blindErrorMessage(t *testing.T, stderr string) string {
	t.Helper()

	var envelope struct {
		Error stack.Error `json:"error"`
	}
	if err := json.Unmarshal([]byte(stderr), &envelope); err != nil {
		t.Fatal(err, stderr)
	}

	return envelope.Error.Message
}

func TestBlindCredentialSymlinkCheckout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privilege")
	}

	f := newTeam(t)
	if err := os.Mkdir(filepath.Join(f.project, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(f.project, link); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := f.run(f.orchFile, f.scoped("agent", "register", "--name", "03-peer", "--new-credential-file", filepath.Join(link, "peer.json"), "--operation-id", "blind-git-link")...)
	if code == 0 || !strings.Contains(stderr, "Git checkout") {
		t.Fatal(code, stderr)
	}
}

func TestBlindViewRevokeAgent(t *testing.T) {
	f := newTeam(t)
	_, stderr, code := f.run(f.workerFile, "view", "revoke", "--all")
	if code == 0 || !strings.Contains(stderr, "Operator") {
		t.Fatal(code, stderr)
	}
}
