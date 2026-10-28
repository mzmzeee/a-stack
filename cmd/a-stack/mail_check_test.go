package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// mailCheckFixture starts a service with one Orchestrator assigned to n new
// Tasks. send adds a message to a Task; check runs the CLI mail check as the
// Orchestrator, with only its credential file for context, from args.
func mailCheckFixture(t *testing.T, n int) (tasks []string, send func(task, body string), check func(want int, flags ...string) mailCheckOutput, args []string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	service, err := stack.Start(ctx, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- service.Serve() }()
	t.Cleanup(func() {
		service.Close(ctx)
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	operator, err := stack.NewClient(dir, "", stack.OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}
	ops := 0
	mutate := func(client *stack.Client, path string, request any) stack.Receipt {
		t.Helper()
		data, err := client.Do(ctx, "POST", path, request)
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Data stack.Receipt `json:"data"`
		}
		if json.Unmarshal(data, &envelope) != nil {
			t.Fatal("receipt")
		}
		return envelope.Data
	}
	op := func() string { ops++; return fmt.Sprintf("mail-check-op-%04d", ops) }
	workspace := mutate(operator, "/v1/workspace/create", stack.WorkspaceCreateRequest{OperationID: op(), Path: root})
	session := mutate(operator, "/v1/session/create", stack.SessionCreateRequest{OperationID: op(), WorkspaceID: workspace.ResourceID, Name: "Check"})
	ref := stack.Context{WorkspaceID: workspace.ResourceID, SessionID: session.ResourceID}
	private := filepath.Join(root, "private")
	if stack.MkdirPrivate(private) != nil {
		t.Fatal("private")
	}
	file := filepath.Join(private, "orch.json")
	register := stack.AgentRegisterRequest{Context: ref, OperationID: op(), Name: "01-orch"}
	if register.CredentialHash, err = stack.PrepareAgentCredential(file, operator.Discovery.DatabaseID, "agent register", register.OperationID, register); err != nil {
		t.Fatal(err)
	}
	orch := mutate(operator, "/v1/agent/register", register)
	mutate(operator, "/v1/session/appoint", stack.SessionChangeRequest{Context: ref, OperationID: op(), ExpectedRevision: 1, AgentID: orch.ResourceID})
	client, err := stack.NewClient(dir, file, stack.AgentCapability)
	if err != nil {
		t.Fatal(err)
	}
	for range n {
		task := mutate(client, "/v1/task/create", stack.TaskCreateRequest{Context: ref, OperationID: op(), Title: "Busy"})
		mutate(client, "/v1/task/assign", stack.TaskChangeRequest{Context: ref, TaskID: task.ResourceID, OperationID: op(), ExpectedRevision: 1, AgentID: orch.ResourceID})
		tasks = append(tasks, task.ResourceID)
	}
	send = func(task, body string) {
		mutate(client, "/v1/mail/send", stack.MailSendRequest{Context: ref, OperationID: op(), TaskID: task, Body: body})
	}
	args = []string{"mail", "check", "--state-dir", dir, "--as", "agent", "--credential-file", file}
	check = func(want int, flags ...string) mailCheckOutput {
		t.Helper()
		var out, errs bytes.Buffer
		if code := run(append(append([]string{}, args...), flags...), &out, &errs); code != want {
			t.Fatalf("mail check exit %d: %s", code, errs.String())
		}
		var envelope struct {
			Data mailCheckOutput `json:"data"`
		}
		if want == 0 && json.Unmarshal(out.Bytes(), &envelope) != nil {
			t.Fatal("mail check output")
		}
		return envelope.Data
	}
	return tasks, send, check, args
}

// newest returns the highest sequence that a check returned for a Task.
func newest(page mailCheckOutput, task string) int64 {
	last := int64(0)
	for _, p := range page.Tasks {
		for _, m := range p.Messages {
			if p.TaskID == task && m.Sequence > last {
				last = m.Sequence
			}
		}
	}
	return last
}

// The review reproduction: 105 messages on one Task and 1 on another. A plain
// check stops at the 100-message limit every time; with a cursor file the
// second check reaches the newest message, wherever the busy Task sorts.
func TestCLIMailCheckCursorFileReachesNewestMail(t *testing.T) {
	for _, position := range []int{0, 1} {
		t.Run(fmt.Sprint("busy Task at ", position), func(t *testing.T) {
			tasks, send, check, _ := mailCheckFixture(t, 2)
			slices.Sort(tasks)
			busy, quiet := tasks[position], tasks[1-position]
			for i := 1; i <= 105; i++ {
				send(busy, fmt.Sprintf("message %03d", i))
			}
			send(quiet, "quiet")
			for range 2 {
				plain := check(0)
				if newest(plain, busy) >= 105 || !plain.More || plain.Hint == "" {
					t.Fatal("plain check: want the 100-message limit, more and a hint")
				}
			}
			file := filepath.Join(t.TempDir(), "cursors.json")
			first, second := check(0, "--cursor-file", file), check(0, "--cursor-file", file)
			if position == 0 {
				if newest(second, quiet) != 1 || !second.More {
					t.Fatal("round must visit quiet Task before revisiting busy Task")
				}
				second = check(0, "--cursor-file", file)
			}
			if !first.More || second.More || newest(second, busy) != 105 || first.Hint != "" {
				t.Fatal("bounded round must reach message 105, and more must say so")
			}
			if info, err := os.Stat(file); err != nil || posixModes && info.Mode().Perm() != 0600 {
				t.Fatal("cursor file mode")
			}
			if page := check(0, "--cursor-file", file); len(page.Tasks) != 0 || page.More {
				t.Fatal("third check showed old mail")
			}
			send(busy, "one more")
			if page := check(0, "--cursor-file", file); len(page.Tasks) != 1 || newest(page, busy) != 106 {
				t.Fatal("new mail after a saved position")
			}
		})
	}
	// A cursor file in a missing folder cannot be written.
	_, _, check, _ := mailCheckFixture(t, 1)
	check(2, "--cursor-file", filepath.Join(t.TempDir(), "missing", "cursors.json"))
}

// With more Tasks than one page scans, the file keeps the scan position, so
// each check goes on with the next Tasks instead of the first ones again.
func TestCLIMailCheckCursorFileFollowsTheTaskScan(t *testing.T) {
	tasks, send, check, _ := mailCheckFixture(t, 3)
	for _, task := range tasks {
		send(task, "hello")
	}
	file := filepath.Join(t.TempDir(), "cursors.json")
	seen := map[string]bool{}
	for i := range 3 {
		page := check(0, "--cursor-file", file, "--task-limit", "1")
		if len(page.Tasks) != 1 || page.NextTask != "" || page.More != (i < 2) {
			t.Fatalf("check %d: one Task per page, more until the last", i+1)
		}
		seen[page.Tasks[0].TaskID] = true
	}
	if len(seen) != 3 {
		t.Fatal("the scan did not reach every Task")
	}
	if page := check(0, "--cursor-file", file, "--task-limit", "1"); len(page.Tasks) != 0 {
		t.Fatal("a new round showed old mail")
	}
}

// Checks that write the same file at once leave a whole file: the last write
// wins, and a lost write only shows its mail again.
func TestCLIMailCheckCursorFileConcurrentWriters(t *testing.T) {
	tasks, send, check, args := mailCheckFixture(t, 2)
	file := filepath.Join(t.TempDir(), "cursors.json")
	args = append(args, "--cursor-file", file)
	for round := range 5 {
		send(tasks[round%2], "round")
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				var out, errs bytes.Buffer
				if run(args, &out, &errs) != 0 {
					t.Error("concurrent check failed:", errs.String())
				}
			})
		}
		wg.Wait()
		var saved map[string]string
		data, err := os.ReadFile(file)
		if err != nil || json.Unmarshal(data, &saved) != nil || len(saved) != 3 {
			t.Fatal("concurrent writers broke the cursor file")
		}
	}
	if page := check(0, "--cursor-file", file); len(page.Tasks) != 0 {
		t.Fatal("old mail after concurrent checks")
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(file), ".a-stack-*"))
	if len(matches) != 0 {
		t.Fatal("temporary files left behind")
	}
}
