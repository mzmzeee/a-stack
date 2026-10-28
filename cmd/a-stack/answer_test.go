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

func TestCLIAnswerIsTheOperatorsAndReachesTheAgentAsMail(t *testing.T) {
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
		if err := service.Close(ctx); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	project, private := filepath.Join(root, "project"), filepath.Join(root, "credentials")
	if os.Mkdir(project, 0700) != nil || stack.MkdirPrivate(private) != nil {
		t.Fatal("fixture directories")
	}
	// invoke runs the CLI as the Operator, or as the Agent of a credential
	// file, and returns stdout on success or stderr on failure.
	invoke := func(actor string, want int, args ...string) []byte {
		t.Helper()
		args = append(args, "--state-dir", dir)
		if actor != "" {
			args = append(args, "--as", "agent", "--credential-file", actor)
		}
		var out, errout bytes.Buffer
		if code := run(args, &out, &errout); code != want {
			t.Fatalf("%v returned %d, want %d: %s", args, code, want, errout.String())
		}
		if want != 0 {
			return errout.Bytes()
		}
		return out.Bytes()
	}
	receipt := func(actor string, args ...string) stack.Receipt {
		t.Helper()
		var r struct{ Data stack.Receipt }
		if json.Unmarshal(invoke(actor, 0, args...), &r) != nil || r.Data.ResourceID == "" {
			t.Fatal("receipt")
		}
		return r.Data
	}
	workspace := receipt("", "workspace", "create", "--path", project, "--operation-id", "answer-workspace")
	session := receipt("", "session", "create", "--workspace", workspace.ResourceID, "--name", "Answers", "--operation-id", "answer-session")
	scoped := func(args ...string) []string {
		return append(args, "--workspace", workspace.ResourceID, "--session", session.ResourceID)
	}
	orchFile, workerFile := filepath.Join(private, "orchestrator.json"), filepath.Join(private, "worker.json")
	orch := receipt("", scoped("agent", "register", "--name", "01-orchestrator", "--new-credential-file", orchFile, "--operation-id", "answer-orchestrator")...)
	receipt("", scoped("session", "appoint", "--agent", orch.ResourceID, "--expected-revision", "1", "--operation-id", "answer-appoint")...)
	worker := receipt(orchFile, scoped("agent", "register", "--name", "02-worker", "--new-credential-file", workerFile, "--operation-id", "answer-worker")...)
	task := receipt(orchFile, scoped("task", "create", "--title", "Login", "--operation-id", "answer-task")...)
	receipt(orchFile, scoped("task", "assign", "--task", task.ResourceID, "--agent", worker.ResourceID, "--expected-revision", "1", "--operation-id", "answer-assign")...)
	receipt(workerFile, scoped("task", "attention", "set", "--task", task.ResourceID, "--question", "GitHub or Google first?", "--expected-revision", "2", "--operation-id", "answer-ask")...)
	var shown struct{ Data stack.TaskDetail }
	if json.Unmarshal(invoke("", 0, scoped("task", "show", "--task", task.ResourceID)...), &shown) != nil || shown.Data.Question == nil {
		t.Fatal("task show")
	}
	question := shown.Data.Question.ID
	answer := func(actor string, want int, extra ...string) []byte {
		t.Helper()
		return invoke(actor, want, scoped(append([]string{"answer", "--task", task.ResourceID, "--question-id", question}, extra...)...)...)
	}
	// Agents cannot answer, not even the asker or the Orchestrator.
	for _, file := range []string{workerFile, orchFile} {
		if out := answer(file, 1, "--text", "Google", "--operation-id", "answer-by-agent"); !strings.Contains(string(out), `"forbidden"`) {
			t.Fatalf("Agent answer: %s", out)
		}
	}
	// Flag checks run before the service sees the answer.
	answer("", 2, "--text", "Google", "--body-file", "-", "--operation-id", "answer-two-texts")
	answer("", 2, "--text", "Google", "--body", "Google", "--operation-id", "answer-text-and-body")
	if out := answer("", 2, "--body", "A\x1b[31mRED", "--operation-id", "answer-escape"); !strings.Contains(string(out), "answer must be plain text without control characters") {
		t.Fatalf("answer with an escape: %s", out)
	}
	invoke("", 2, scoped("answer", "--task", task.ResourceID, "--question-id", "not an id", "--text", "x", "--operation-id", "answer-bad-id")...)
	answer("", 2, "--text", "Google", "--expected-revision", "3", "--operation-id", "answer-revision")
	if out := answer("", 2, "--text", strings.Repeat("x", stack.MaxAnswerBytes+1), "--operation-id", "answer-long"); !strings.Contains(string(out), "4096 bytes") {
		t.Fatalf("long answer: %s", out)
	}
	text := filepath.Join(root, "answer.txt")
	if os.WriteFile(text, []byte("Google first.\nGitHub next week."), 0600) != nil {
		t.Fatal("answer file")
	}
	answered := receipt("", scoped("answer", "--task", task.ResourceID, "--question-id", question, "--body-file", text, "--operation-id", "answer-1")...)
	if answered.Outcome != "answered" || answered.Revision != 4 || receipt("", scoped("answer", "--task", task.ResourceID, "--question-id", question, "--body-file", text, "--operation-id", "answer-1")...) != answered {
		t.Fatal("answer receipt or retry", answered)
	}
	if out := answer("", 1, "--body", "Again", "--operation-id", "answer-2"); !strings.Contains(string(out), "already answered by Operator at ") {
		t.Fatalf("second answer: %s", out)
	}
	// The Agent finds the answer in mail check and in task show.
	var mail struct{ Data stack.MailCheckPage }
	if json.Unmarshal(invoke(workerFile, 0, scoped("mail", "check")...), &mail) != nil || len(mail.Data.Tasks) != 1 {
		t.Fatal("mail check")
	}
	if m := mail.Data.Tasks[0].Messages; len(m) != 1 || m[0].SenderKind != "operator" || m[0].SenderName != "Operator" || m[0].Body != "Google first.\nGitHub next week." {
		t.Fatalf("mail: %+v", m)
	}
	if json.Unmarshal(invoke(workerFile, 0, scoped("task", "show", "--task", task.ResourceID)...), &shown) != nil || shown.Data.NeedsYou || shown.Data.AnsweredQuestion == nil || shown.Data.AnsweredQuestion.Answer != "Google first.\nGitHub next week." {
		t.Fatalf("task show after the answer: %+v", shown.Data)
	}
}
