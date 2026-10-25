package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestCLIMailInputIndependentCursorsRemovalAndRejoin(t *testing.T) {
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
	workspace := mutate(operator, "/v1/workspace/create", stack.WorkspaceCreateRequest{OperationID: "mail-cli-workspace", Path: root})
	session := mutate(operator, "/v1/session/create", stack.SessionCreateRequest{OperationID: "mail-cli-session", WorkspaceID: workspace.ResourceID, Name: "Mail"})
	ref := stack.Context{WorkspaceID: workspace.ResourceID, SessionID: session.ResourceID}
	private := filepath.Join(root, "credentials")
	if os.Mkdir(private, 0700) != nil {
		t.Fatal("credentials")
	}
	enroll := func(name string) (string, stack.Receipt) {
		t.Helper()
		path := filepath.Join(private, name+".json")
		request := stack.AgentRegisterRequest{Context: ref, OperationID: "mail-cli-enroll-" + name, Name: name}
		hash, err := stack.PrepareAgentCredential(path, operator.Discovery.DatabaseID, "agent register", request.OperationID, request)
		if err != nil {
			t.Fatal(err)
		}
		request.CredentialHash = hash
		return path, mutate(operator, "/v1/agent/register", request)
	}
	orchFile, orch := enroll("01-orchestrator")
	aFile, a := enroll("02-worker")
	bFile, b := enroll("03-worker")
	mutate(operator, "/v1/session/appoint", stack.SessionChangeRequest{Context: ref, OperationID: "mail-cli-appoint", ExpectedRevision: 1, AgentID: orch.ResourceID})
	orchestrator, err := stack.NewClient(dir, orchFile, stack.AgentCapability)
	if err != nil {
		t.Fatal(err)
	}
	task := mutate(orchestrator, "/v1/task/create", stack.TaskCreateRequest{Context: ref, OperationID: "mail-cli-task", Title: "Mail CLI"})
	mutate(orchestrator, "/v1/task/assign", stack.TaskChangeRequest{Context: ref, TaskID: task.ResourceID, OperationID: "mail-cli-assign-a", ExpectedRevision: 1, AgentID: a.ResourceID})
	mutate(orchestrator, "/v1/task/assign", stack.TaskChangeRequest{Context: ref, TaskID: task.ResourceID, OperationID: "mail-cli-assign-b", ExpectedRevision: 2, AgentID: b.ResourceID})
	args := func(actor string, command ...string) []string {
		result := append([]string{}, command...)
		result = append(result, "--state-dir", dir, "--json", "--workspace", ref.WorkspaceID, "--session", ref.SessionID)
		if actor != "" {
			result = append(result, "--as", "agent", "--credential-file", actor)
		}
		return result
	}
	invoke := func(command []string, want int) json.RawMessage {
		t.Helper()
		var out, errs bytes.Buffer
		code := run(command, &out, &errs)
		if code != want {
			t.Fatalf("unexpected CLI exit %d, want %d: %s", code, want, errs.String())
		}
		if want != 0 {
			if out.Len() != 0 || !json.Valid(errs.Bytes()) {
				t.Fatal("error streams")
			}
			return errs.Bytes()
		}
		if errs.Len() != 0 {
			t.Fatal("success stderr")
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(out.Bytes(), &envelope) != nil {
			t.Fatal("JSON stdout")
		}
		return envelope.Data
	}
	bodyPath := filepath.Join(root, "message.txt")
	body := "Hello @03-worker\nliteral $(nothing-to-execute)"
	if os.WriteFile(bodyPath, []byte(body), 0600) != nil {
		t.Fatal("body file")
	}
	send := args(aFile, "mail", "send", "--task", task.ResourceID, "--operation-id", "mail-cli-first-send", "--body-file", bodyPath)
	firstData := invoke(send, 0)
	var first stack.Receipt
	if json.Unmarshal(firstData, &first) != nil || first.Revision != 1 {
		t.Fatal("first send")
	}
	retryData := invoke(send, 0)
	var retry stack.Receipt
	if json.Unmarshal(retryData, &retry) != nil || retry != first {
		t.Fatal("CLI retry duplicated send")
	}
	read := func(file, cursor string) stack.MailPage {
		t.Helper()
		command := args(file, "mail", "read", "--task", task.ResourceID)
		if cursor != "" {
			command = append(command, "--cursor", cursor)
		}
		data := invoke(command, 0)
		var page stack.MailPage
		if json.Unmarshal(data, &page) != nil {
			t.Fatal("read JSON")
		}
		return page
	}
	aPage, bPage := read(aFile, ""), read(bFile, "")
	if len(aPage.Messages) != 1 || len(bPage.Messages) != 1 || aPage.Messages[0].Body != body || aPage.Cursor != bPage.Cursor {
		t.Fatal("CLI shared history/body file")
	}
	invoke(args(bFile, "mail", "send", "--task", task.ResourceID, "--operation-id", "mail-cli-reply", "--body", "Reply", "--mention", "02-worker", "--mention", "02-worker"), 0)
	if len(read(aFile, aPage.Cursor).Messages) != 1 || len(read(bFile, "").Messages) != 2 {
		t.Fatal("independent CLI positions")
	}
	cursors := map[string]string{task.ResourceID: aPage.Cursor}
	cursorData, _ := json.Marshal(cursors)
	cursorFile := filepath.Join(root, "cursors.json")
	if os.WriteFile(cursorFile, cursorData, 0600) != nil {
		t.Fatal("cursor file")
	}
	checkData := invoke(args(aFile, "mail", "check", "--cursor-file", cursorFile), 0)
	var check stack.MailCheckPage
	if json.Unmarshal(checkData, &check) != nil || len(check.Tasks) != 1 || len(check.Tasks[0].Messages) != 1 {
		t.Fatal("CLI check cursors")
	}
	var saved map[string]string
	after, _ := os.ReadFile(cursorFile)
	if json.Unmarshal(after, &saved) != nil || len(saved) != 1 || saved[task.ResourceID] != check.Tasks[0].Cursor {
		t.Fatal("CLI did not save the returned cursor")
	}
	if checkData = invoke(args(aFile, "mail", "check", "--cursor-file", cursorFile), 0); json.Unmarshal(checkData, &check) != nil || len(check.Tasks) != 0 || check.More {
		t.Fatal("saved cursor showed old mail again")
	}
	command := child(t, args(bFile, "mail", "send", "--task", task.ResourceID, "--operation-id", "mail-cli-stdin", "--body-file", "-")...)
	command.Stdin = strings.NewReader("Stdin body @02-worker\nsecond line")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if command.Run() != nil || !json.Valid(stdout.Bytes()) || stderr.Len() != 0 {
		t.Fatal("mail stdin subprocess")
	}
	invoke(args("", "mail", "read", "--task", task.ResourceID), 1)
	invoke(args(orchFile, "mail", "read", "--task", task.ResourceID), 1)
	invoke(args(aFile, "mail", "send", "--task", task.ResourceID, "--operation-id", "mail-cli-invalid-utf8", "--body", string([]byte{255})), 2)
	mutate(orchestrator, "/v1/task/unassign", stack.TaskChangeRequest{Context: ref, TaskID: task.ResourceID, OperationID: "mail-cli-remove-a", ExpectedRevision: 3, AgentID: a.ResourceID})
	invoke(args(aFile, "mail", "read", "--task", task.ResourceID), 1)
	invoke(send, 1)
	checkData = invoke(args(aFile, "mail", "check", "--cursor-file", cursorFile), 0)
	if json.Unmarshal(checkData, &check) != nil || len(check.Tasks) != 0 {
		t.Fatal("CLI check leaked removed Task")
	}
	invoke(args(bFile, "mail", "send", "--task", task.ResourceID, "--operation-id", "mail-cli-while-away", "--body", "While A is away"), 0)
	mutate(orchestrator, "/v1/task/assign", stack.TaskChangeRequest{Context: ref, TaskID: task.ResourceID, OperationID: "mail-cli-rejoin-a", ExpectedRevision: 4, AgentID: a.ResourceID})
	if len(read(aFile, "").Messages) != 4 {
		t.Fatal("rejoin lost mail interval")
	}
	mutate(operator, "/v1/session/close", stack.SessionChangeRequest{Context: ref, OperationID: "mail-cli-close", ExpectedRevision: 2})
	if len(read(aFile, "").Messages) != 4 {
		t.Fatal("closed CLI read")
	}
	invoke(send, 0)
	invoke(args(aFile, "mail", "send", "--task", task.ResourceID, "--operation-id", "mail-cli-closed-send", "--body", "Closed write"), 1)
}
