package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestCLITasksSharedGraphAttentionAndClosedSession(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	start := func() {
		t.Helper()
		service, err := stack.Start(ctx, dir, "")
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- service.Serve() }()
		t.Cleanup(func() {
			if err := service.Close(ctx); err != nil {
				t.Error(err)
			}
			if err := <-done; err != nil {
				t.Error(err)
			}
		})
	}
	start()
	project := filepath.Join(root, "project")
	private := filepath.Join(root, "credentials")
	if os.Mkdir(project, 0700) != nil || os.Mkdir(private, 0700) != nil {
		t.Fatal("fixture directories")
	}
	invoke := func(actor string, want int, args ...string) json.RawMessage {
		t.Helper()
		command := append([]string{}, args...)
		command = append(command, "--state-dir", dir, "--json")
		if actor != "" {
			command = append(command, "--as", "agent", "--credential-file", actor)
		}
		var out, errout bytes.Buffer
		code := run(command, &out, &errout)
		if code != want {
			t.Fatalf("%s returned %d, want %d; diagnostic %s", strings.Join(args[:2], " "), code, want, errout.String())
		}
		if want != 0 {
			if out.Len() != 0 || !json.Valid(errout.Bytes()) {
				t.Fatal("CLI error streams")
			}
			return errout.Bytes()
		}
		if errout.Len() != 0 {
			t.Fatal("CLI success diagnostics")
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(out.Bytes(), &envelope) != nil {
			t.Fatal("CLI JSON")
		}
		return envelope.Data
	}
	receipt := func(actor string, args ...string) stack.Receipt {
		t.Helper()
		data := invoke(actor, 0, args...)
		var r stack.Receipt
		if json.Unmarshal(data, &r) != nil || r.ResourceID == "" {
			t.Fatal("mutation receipt")
		}
		return r
	}
	workspace := receipt("", "workspace", "create", "--path", project, "--operation-id", "task-cli-workspace")
	session := receipt("", "session", "create", "--workspace", workspace.ResourceID, "--name", "Tasks", "--operation-id", "task-cli-session")
	ref := []string{"--workspace", workspace.ResourceID, "--session", session.ResourceID}
	scoped := func(args ...string) []string { return append(args, ref...) }
	orchFile := filepath.Join(private, "orchestrator.json")
	aFile := filepath.Join(private, "worker-a.json")
	bFile := filepath.Join(private, "worker-b.json")
	orch := receipt("", scoped("agent", "register", "--name", "01-orchestrator", "--new-credential-file", orchFile, "--operation-id", "task-cli-orchestrator")...)
	receipt("", scoped("session", "appoint", "--agent", orch.ResourceID, "--expected-revision", "1", "--operation-id", "task-cli-appoint")...)
	a := receipt(orchFile, scoped("agent", "register", "--name", "02-worker", "--new-credential-file", aFile, "--operation-id", "task-cli-worker-a")...)
	b := receipt(orchFile, scoped("agent", "register", "--name", "03-worker", "--new-credential-file", bFile, "--operation-id", "task-cli-worker-b")...)
	createArgs := scoped("task", "create", "--title", "Shared Task", "--description", "Whole-team outcome", "--ticket", "#4", "--operation-id", "task-cli-shared")
	shared := receipt(orchFile, createArgs...)
	if receipt(orchFile, createArgs...) != shared {
		t.Fatal("CLI create retry")
	}
	invoke(orchFile, 1, scoped("task", "status", "--task", shared.ResourceID, "--expected-revision", "1", "--progress", "working", "--operation-id", "task-cli-unassigned")...)
	receipt(orchFile, scoped("task", "assign", "--task", shared.ResourceID, "--agent", a.ResourceID, "--expected-revision", "1", "--operation-id", "task-cli-assign-a")...)
	receipt(orchFile, scoped("task", "assign", "--task", shared.ResourceID, "--agent", b.ResourceID, "--expected-revision", "2", "--operation-id", "task-cli-assign-b")...)
	working := receipt(aFile, scoped("task", "status", "--task", shared.ResourceID, "--expected-revision", "3", "--progress", "working", "--operation-id", "task-cli-working")...)
	stale := invoke(bFile, 1, scoped("task", "status", "--task", shared.ResourceID, "--expected-revision", "3", "--progress", "working", "--operation-id", "task-cli-stale")...)
	var staleError struct {
		Error stack.Error `json:"error"`
	}
	if json.Unmarshal(stale, &staleError) != nil || staleError.Error.Code != "conflict" || staleError.Error.CurrentRevision == nil || *staleError.Error.CurrentRevision != working.Revision {
		t.Fatal("CLI stale conflict")
	}
	receipt(orchFile, scoped("agent", "attach", "--agent", orch.ResourceID, "--runtime-id", "cli-unknown-orchestrator", "--expected-revision", "1", "--operation-id", "task-cli-runtime")...)
	invoke(orchFile, 0, scoped("agent", "observe", "--agent", orch.ResourceID, "--runtime-id", "cli-unknown-orchestrator", "--expected-revision", "2", "--runtime-state", "unknown")...)
	bodyPath := filepath.Join(root, "report.txt")
	body := "Report with a newline\nliteral $(nothing-to-execute) <text>"
	if os.WriteFile(bodyPath, []byte(body), 0600) != nil {
		t.Fatal("body fixture")
	}
	reported := receipt(aFile, scoped("task", "report", "--task", shared.ResourceID, "--expected-revision", "4", "--body-file", bodyPath, "--operation-id", "task-cli-report")...)
	question := receipt(bFile, scoped("task", "attention", "set", "--task", shared.ResourceID, "--expected-revision", strconv.FormatInt(reported.Revision, 10), "--question", "Which release?", "--operation-id", "task-cli-question")...)
	completed := receipt(aFile, scoped("task", "status", "--task", shared.ResourceID, "--expected-revision", strconv.FormatInt(question.Revision, 10), "--progress", "finished", "--summary", "Whole Task delivered", "--operation-id", "task-cli-finish")...)
	reopened := receipt(bFile, scoped("task", "reopen", "--task", shared.ResourceID, "--expected-revision", strconv.FormatInt(completed.Revision, 10), "--reason", "Revisit approach", "--operation-id", "task-cli-reopen")...)
	cancelled := receipt(aFile, scoped("task", "status", "--task", shared.ResourceID, "--expected-revision", strconv.FormatInt(reopened.Revision, 10), "--progress", "cancelled", "--reason", "Old approach abandoned", "--operation-id", "task-cli-cancel")...)
	second := receipt(orchFile, scoped("task", "create", "--title", "Dependent B", "--operation-id", "task-cli-second")...)
	third := receipt(orchFile, scoped("task", "create", "--title", "Dependent C", "--operation-id", "task-cli-third")...)
	receipt(orchFile, scoped("dependency", "add", "--from", shared.ResourceID, "--to", second.ResourceID, "--expected-revision", "4", "--operation-id", "task-cli-edge-ab")...)
	receipt(orchFile, scoped("dependency", "add", "--from", shared.ResourceID, "--to", third.ResourceID, "--expected-revision", "5", "--operation-id", "task-cli-edge-ac")...)
	// Race two individually plausible edges through the CLI parser and real HTTP.
	var wg sync.WaitGroup
	exits := make(chan int, 2)
	for i, pair := range [][2]string{{second.ResourceID, third.ResourceID}, {third.ResourceID, second.ResourceID}} {
		wg.Go(func() {
			command := scoped("dependency", "add", "--from", pair[0], "--to", pair[1], "--expected-revision", "6", "--operation-id", "task-cli-edge-race-"+strconv.Itoa(i))
			command = append(command, "--state-dir", dir, "--json", "--as", "agent", "--credential-file", orchFile)
			var out, errs bytes.Buffer
			exits <- run(command, &out, &errs)
		})
	}
	wg.Wait()
	close(exits)
	won, lost := 0, 0
	for code := range exits {
		if code == 0 {
			won++
		} else if code == 1 {
			lost++
		} else {
			t.Fatal("CLI graph race invalid exit")
		}
	}
	if won != 1 || lost != 1 {
		t.Fatal("CLI graph race did not serialize")
	}
	invoke(orchFile, 1, scoped("dependency", "add", "--from", third.ResourceID, "--to", shared.ResourceID, "--expected-revision", "7", "--operation-id", "task-cli-cycle")...)
	receipt(orchFile, scoped("task", "assign", "--task", second.ResourceID, "--agent", b.ResourceID, "--expected-revision", "1", "--operation-id", "task-cli-assign-dependent")...)
	receipt(bFile, scoped("task", "status", "--task", second.ResourceID, "--expected-revision", "2", "--progress", "finished", "--summary", "Finished despite cancelled prerequisite", "--operation-id", "task-cli-dependent-finish")...)
	data := invoke(aFile, 0, scoped("task", "show", "--task", shared.ResourceID)...)
	var shown stack.TaskDetail
	if json.Unmarshal(data, &shown) != nil || shown.Question == nil || shown.Report != body || shown.Result != "Whole Task delivered" || shown.Progress != "cancelled" {
		t.Fatal("CLI attention/outcome/report state")
	}
	resolved := receipt(aFile, scoped("task", "attention", "resolve", "--task", shared.ResourceID, "--expected-revision", strconv.FormatInt(cancelled.Revision, 10), "--note", "Operator selected v1", "--operation-id", "task-cli-resolve")...)
	reference := receipt(aFile, scoped("task", "reference", "add", "--task", shared.ResourceID, "--expected-revision", strconv.FormatInt(resolved.Revision, 10), "--location", "docs/evidence.md", "--reference-revision", "v1", "--operation-id", "task-cli-reference")...)
	refsData := invoke(aFile, 0, scoped("task", "references", "--task", shared.ResourceID)...)
	var refs struct {
		Items []stack.FileReference `json:"items"`
	}
	if json.Unmarshal(refsData, &refs) != nil || len(refs.Items) != 1 {
		t.Fatal("CLI reference page")
	}
	removed := receipt(aFile, scoped("task", "reference", "remove", "--task", shared.ResourceID, "--expected-revision", strconv.FormatInt(reference.Revision, 10), "--reference", refs.Items[0].ID, "--operation-id", "task-cli-remove-reference")...)
	// Actual subprocess/stdin exercises --body-file - without shell interpolation.
	command := child(t, scoped("task", "report", "--task", shared.ResourceID, "--expected-revision", strconv.FormatInt(removed.Revision, 10), "--body-file", "-", "--operation-id", "task-cli-stdin", "--state-dir", dir, "--json", "--as", "agent", "--credential-file", aFile)...)
	command.Stdin = strings.NewReader("Stdin report\nsecond line")
	var out, errs bytes.Buffer
	command.Stdout = &out
	command.Stderr = &errs
	if command.Run() != nil || !json.Valid(out.Bytes()) || errs.Len() != 0 {
		t.Fatal("CLI stdin report")
	}
	invoke(bFile, 0, scoped("task", "history", "--task", shared.ResourceID)...)
	invoke(bFile, 0, scoped("task", "list", "--limit", "2")...)
	invoke(bFile, 0, scoped("session", "snapshot")...)
	receipt("", scoped("session", "close", "--expected-revision", "2", "--operation-id", "task-cli-close")...)
	invoke(aFile, 1, scoped("task", "status", "--task", shared.ResourceID, "--expected-revision", strconv.FormatInt(removed.Revision+1, 10), "--progress", "working", "--operation-id", "task-cli-closed-update")...)
	receipt("", scoped("session", "reopen", "--expected-revision", "3", "--operation-id", "task-cli-reopen-session")...)
	data = invoke(aFile, 0, scoped("task", "show", "--task", shared.ResourceID)...)
	if json.Unmarshal(data, &shown) != nil || shown.Progress != "cancelled" || shown.Report != "Stdin report\nsecond line" || shown.NeedsYou {
		t.Fatal("closure/reopen changed Task fields")
	}
}
