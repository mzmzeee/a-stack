package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func receiptResult(t *testing.T, args []string) stack.Receipt {
	t.Helper()
	data, _ := commandResult(t, args, 0)
	var envelope struct {
		Data stack.Receipt `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.Data.ResourceID == "" {
		t.Fatal("invalid mutation receipt")
	}
	return envelope.Data
}
func TestCLIEnrollmentAppointmentRebindAndLifecycle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	process := launch(t, dir)
	root := t.TempDir()
	private := filepath.Join(root, "credentials")
	if os.Mkdir(private, 0700) != nil {
		t.Fatal("private credential dir")
	}
	common := func(args ...string) []string { return append(args, "--state-dir", dir, "--json") }
	workspace := receiptResult(t, common("workspace", "create", "--path", root, "--operation-id", "cli-workspace"))
	session := receiptResult(t, common("session", "create", "--workspace", workspace.ResourceID, "--name", "First", "--operation-id", "cli-session"))
	other := receiptResult(t, common("session", "create", "--workspace", workspace.ResourceID, "--name", "Second", "--operation-id", "cli-other-session"))
	ref := []string{"--workspace", workspace.ResourceID, "--session", session.ResourceID}
	withRef := func(args ...string) []string { return common(append(args, ref...)...) }
	orchFile := filepath.Join(private, "orchestrator.json")
	enroll := withRef("agent", "register", "--name", "01-orchestrator", "--operation-id", "cli-enroll-orch", "--new-credential-file", orchFile)
	orchestrator := receiptResult(t, enroll)
	replayed := receiptResult(t, enroll)
	if replayed != orchestrator {
		t.Fatal("enrollment duplicated")
	}
	orch := func(args ...string) []string {
		return withRef(append(args, "--as", "agent", "--credential-file", orchFile)...)
	}
	receiptResult(t, orch("agent", "attach", "--agent", orchestrator.ResourceID, "--operation-id", "cli-attach-orch", "--expected-revision", "1", "--runtime-id", "cli-orchestrator-runtime"))
	appointment := receiptResult(t, withRef("session", "appoint", "--agent", orchestrator.ResourceID, "--operation-id", "cli-appoint-orch", "--expected-revision", "1"))
	if appointment.Revision != 2 {
		t.Fatal("appointment revision")
	}
	workerFile := filepath.Join(private, "worker.json")
	worktree := t.TempDir()
	worker := receiptResult(t, orch("agent", "register", "--name", "02-worker", "--worktree", worktree, "--operation-id", "cli-enroll-worker", "--new-credential-file", workerFile))
	workerArgs := func(file string, args ...string) []string {
		return withRef(append(args, "--as", "agent", "--credential-file", file)...)
	}
	receiptResult(t, workerArgs(workerFile, "agent", "attach", "--agent", worker.ResourceID, "--operation-id", "cli-attach-worker", "--expected-revision", "1", "--runtime-id", "cli-worker-runtime"))
	newFile := filepath.Join(private, "worker-rebound.json")
	rebind := withRef("agent", "rebind", "--agent", worker.ResourceID, "--operation-id", "cli-rebind-worker", "--expected-revision", "2", "--runtime-id", "cli-new-runtime", "--new-credential-file", newFile)
	rebound := receiptResult(t, rebind)
	if rebound.ResourceID != worker.ResourceID || rebound.Revision != 3 {
		t.Fatal("rebind changed identity")
	}
	old, _ := os.ReadFile(newFile)
	retried := receiptResult(t, rebind)
	after, _ := os.ReadFile(newFile)
	if retried != rebound || !bytes.Equal(old, after) {
		t.Fatal("rebind retry rotated credential")
	}
	commandResult(t, workerArgs(workerFile, "session", "inspect"), 1)
	commandResult(t, workerArgs(newFile, "session", "inspect"), 0)
	commandResult(t, common("session", "inspect", "--workspace", workspace.ResourceID, "--session", other.ResourceID, "--as", "agent", "--credential-file", newFile), 1)
	commandResult(t, withRef("session", "inspect", "--as", "agent"), 2)
	// A different cwd/worktree never creates or selects another Workspace.
	command := child(t, workerArgs(newFile, "agent", "list")...)
	command.Dir = worktree
	output, err := command.Output()
	if err != nil {
		t.Fatal("explicit context failed in worktree")
	}
	var agents struct {
		Data stack.Page[stack.Agent] `json:"data"`
	}
	if json.Unmarshal(output, &agents) != nil || len(agents.Data.Items) != 2 {
		t.Fatal("shared explicit Workspace team")
	}
	found := false
	for _, a := range agents.Data.Items {
		if a.ID == worker.ResourceID {
			found = a.WorkspaceID == workspace.ResourceID && a.WorktreePath == worktree
		}
	}
	if !found {
		t.Fatal("worktree silently changed Workspace")
	}
	closeArgs := withRef("session", "close", "--operation-id", "cli-close-session", "--expected-revision", strconv.FormatInt(appointment.Revision, 10))
	closed := receiptResult(t, closeArgs)
	if receiptResult(t, closeArgs) != closed {
		t.Fatal("close retry")
	}
	commandResult(t, workerArgs(newFile, "agent", "observe", "--agent", worker.ResourceID, "--expected-revision", "3", "--runtime-id", "cli-new-runtime", "--runtime-state", "done"), 0)
	opened := receiptResult(t, withRef("session", "reopen", "--operation-id", "cli-reopen-session", "--expected-revision", strconv.FormatInt(closed.Revision, 10)))
	if opened.Revision != 4 {
		t.Fatal("reopen revision")
	}
	if process.Process.Signal(syscall.SIGTERM) != nil {
		t.Fatal("stop")
	}
	if process.Wait() != nil {
		t.Fatal("stop failure")
	}
	restarted := launch(t, dir)
	if receiptResult(t, rebind) != rebound {
		t.Fatal("restarted rebind receipt changed")
	}
	commandResult(t, workerArgs(newFile, "session", "inspect"), 0)
	if restarted.Process.Signal(syscall.SIGTERM) != nil {
		t.Fatal("stop restarted")
	}
	if restarted.Wait() != nil {
		t.Fatal("restart stop failure")
	}
}
