// The test needs F_SETPIPE_SZ, which macOS lacks.

//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestCLIReportOpenStdinTerminatesOnSignalsWithoutMutation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	service, err := stack.Start(ctx, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- service.Serve() }()
	t.Cleanup(func() {
		if err := service.Close(ctx); err != nil {
			t.Error(err)
		}
		if err := <-served; err != nil {
			t.Error(err)
		}
	})
	operator, err := stack.NewClient(dir, "", stack.OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}
	mutation := func(client *stack.Client, path string, request any) stack.Receipt {
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
	workspace := mutation(operator, "/v1/workspace/create", stack.WorkspaceCreateRequest{OperationID: "cancel-workspace", Path: root})
	session := mutation(operator, "/v1/session/create", stack.SessionCreateRequest{OperationID: "cancel-session", WorkspaceID: workspace.ResourceID, Name: "Cancellation"})
	ref := stack.Context{WorkspaceID: workspace.ResourceID, SessionID: session.ResourceID}
	private := filepath.Join(root, "credentials")
	if os.Mkdir(private, 0700) != nil {
		t.Fatal("private credential directory")
	}
	credential := filepath.Join(private, "agent.json")
	register := stack.AgentRegisterRequest{Context: ref, OperationID: "cancel-agent-register", Name: "orchestrator"}
	register.CredentialHash, err = stack.PrepareAgentCredential(credential, operator.Discovery.DatabaseID, "agent register", register.OperationID, register)
	if err != nil {
		t.Fatal(err)
	}
	agent := mutation(operator, "/v1/agent/register", register)
	mutation(operator, "/v1/session/appoint", stack.SessionChangeRequest{Context: ref, OperationID: "cancel-appoint", ExpectedRevision: 1, AgentID: agent.ResourceID})
	actor, err := stack.NewClient(dir, credential, stack.AgentCapability)
	if err != nil {
		t.Fatal(err)
	}
	task := mutation(actor, "/v1/task/create", stack.TaskCreateRequest{Context: ref, OperationID: "cancel-task-create", Title: "Cancelled input"})
	mutation(actor, "/v1/task/assign", stack.TaskChangeRequest{Context: ref, OperationID: "cancel-task-assign", TaskID: task.ResourceID, ExpectedRevision: 1, AgentID: agent.ResourceID})
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			input, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			// Linux is the supported target. A write larger than the reduced pipe
			// capacity cannot finish until the child consumes input. This synchronizes
			// on the blocked body reader, rather than guessing its startup time.
			capacity, _, errno := syscall.Syscall(syscall.SYS_FCNTL, writer.Fd(), syscall.F_SETPIPE_SZ, 4096)
			if errno != 0 {
				input.Close()
				writer.Close()
				t.Fatal(errno)
			}
			command := child(t, "task", "report", "--state-dir", dir, "--json", "--as", "agent", "--credential-file", credential, "--workspace", workspace.ResourceID, "--session", session.ResourceID, "--task", task.ResourceID, "--expected-revision", "2", "--operation-id", fmt.Sprintf("cancel-report-%d", sig), "--body-file", "-")
			command.Stdin = input
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			if err = command.Start(); err != nil {
				input.Close()
				writer.Close()
				t.Fatal(err)
			}
			input.Close()
			finished := make(chan error, 1)
			go func() { finished <- command.Wait() }()
			consumedWait := false
			t.Cleanup(func() {
				_ = command.Process.Kill()
				_ = writer.Close()
				if !consumedWait {
					<-finished
				}
			})
			wrote := make(chan error, 1)
			go func() { _, err := writer.Write(bytes.Repeat([]byte("x"), int(capacity)+1)); wrote <- err }()
			select {
			case err = <-wrote:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("child did not begin reading stdin")
			}
			if err = command.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-finished:
				consumedWait = true
				exit, ok := err.(*exec.ExitError)
				if !ok {
					t.Fatal("client did not terminate with an error after signal")
				}
				status, ok := exit.Sys().(syscall.WaitStatus)
				if !ok || !status.Signaled() || status.Signal() != sig {
					t.Fatal("client did not retain normal signal termination")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("signal did not terminate client while stdin remained open")
			}
			// writer is deliberately still open: no EOF released the child.
			if stdout.Len() != 0 {
				t.Fatal("cancelled input produced a successful outcome")
			}
			data, err := actor.Do(ctx, "POST", "/v1/task/show", stack.TaskReadRequest{Context: ref, TaskID: task.ResourceID})
			if err != nil {
				t.Fatal(err)
			}
			var envelope struct {
				Data stack.TaskDetail `json:"data"`
			}
			if json.Unmarshal(data, &envelope) != nil || envelope.Data.Revision != 2 || envelope.Data.Report != "" {
				t.Fatal("cancelled input mutated Task")
			}
		})
	}
}
