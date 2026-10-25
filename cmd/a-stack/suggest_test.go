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

func TestNearestSuggestsCloseWordsOnly(t *testing.T) {
	names := commandNames()
	for word, want := range map[string]string{"tsak list": "task list", "task lst": "task list", "tsak": "task", "sesion inspect": "session inspect", "xyz": "", "task": ""} {
		if got := nearest(word, names); got != want {
			t.Fatalf("nearest(%q) = %q, want %q", word, got, want)
		}
	}
	if distance("ab", "ba") != 1 || distance("", "abc") != 3 || distance("kitten", "sitting") != 3 {
		t.Fatal("edit distance")
	}
}

// cliError runs args without a service and returns the error message.
func cliError(t *testing.T, args ...string) string {
	t.Helper()
	var out, errout bytes.Buffer
	if code := run(args, &out, &errout); code != 2 || out.Len() != 0 {
		t.Fatalf("%v: exit %d stdout %q stderr %s", args, code, out.String(), errout.String())
	}
	var e struct {
		Error stack.Error `json:"error"`
	}
	if json.Unmarshal(errout.Bytes(), &e) != nil || e.Error.Code != "invalid_input" {
		t.Fatalf("%v: stderr %s", args, errout.String())
	}
	return e.Error.Message
}

func TestCLIErrorsNameWhatIsWrong(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	id := strings.Repeat("A", 43)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"task", "list", "--workspce", "x"}, "unknown flag --workspce (did you mean --workspace?); use --help"},
		{[]string{"task", "show", "--task", id, "--expected-revision", "abc"}, `invalid value "abc" for --expected-revision; give a whole number`},
		{[]string{"task", "report", "--progress", "working"}, "--progress does not apply to a-stack task report; see a-stack help task report"},
		{[]string{"task", "report", "--reprot", "x"}, "unknown flag --reprot (did you mean --report?); use --help"},
		{[]string{"mail", "send", "--summary", "x"}, "--summary does not apply to a-stack mail send; see a-stack help mail send"},
		{[]string{"task", "show", "abc"}, "unexpected argument abc for task show (did you mean --task abc?)"},
		{[]string{"mail", "send", "--task", id, "--body", "two", "words"}, "unexpected argument words for mail send; quote text that has spaces, as in --body 'two words'"},
		{[]string{"tsak", "list"}, "unknown command tsak list (did you mean task list?); use --help"},
		{[]string{"task", "lst"}, "unknown command task lst (did you mean task list?); use --help"},
		{[]string{"help", "tsak"}, "no help for tsak (did you mean task?); use --help"},
		{[]string{"task", "attention", "--task", id}, "task attention needs one more word: set, resolve; run a-stack help task attention"},
		{[]string{"hook", "install", "--agent", "01-orch"}, "--agent takes an Agent ID; 01-orch looks like a name; see a-stack agent list"},
		{[]string{"session", "appoint", "--agent", "a b"}, "--agent takes an Agent ID or name; a b is not one; see a-stack agent list"},
		{[]string{"task", "create", "--title", "x", "--assign", "a b"}, "--assign takes an Agent ID or name; a b is not one; see a-stack agent list"},
		{[]string{"task", "create", "--title", "x", "--after", id, "--after", "nope"}, "--after takes a Task ID; nope is not one; see a-stack task list"},
		{[]string{"task", "list", "--after", id, "--after", id}, "--after is given more than once"},
		{[]string{"task", "show", "--task", "nope"}, "--task takes a Task ID; nope is not one; see a-stack task list"},
		{[]string{"task", "show", "--task", ""}, "--task takes a Task ID; it is empty; see a-stack task list"},
		{[]string{"task", "show", "--task"}, "--task requires a value"},
		{[]string{"task", "show", "--task", "--json"}, "--task requires a value"},
		{[]string{"task", "show", "--task", id, "--task", id}, "--task is given more than once"},
		{[]string{"version", "--as", "agent"}, "--as does not apply to a-stack version; see a-stack help version"},
		{[]string{"--version", "--check"}, "--check does not apply to a-stack version; see a-stack help version"},
		{[]string{"task", "status", "--workspace", id, "--session", id, "--task", id, "--progress", "waiting", "--expected-revision", "1", "--operation-id", "status-1"}, "--reason is required for --progress waiting"},
		{[]string{"task", "status", "--workspace", id, "--session", id, "--task", id, "--progress", "finished", "--body", " ", "--expected-revision", "1", "--operation-id", "status-1"}, "--summary is required for --progress finished"},
		{[]string{"task", "report", "--workspace", id, "--session", id, "--task", id, "--report", "a", "--body", "b"}, "choose one of --report, --body and --body-file"},
	} {
		if got := cliError(t, append(c.args, "--state-dir", state)...); got != c.want {
			t.Fatalf("%v:\n got %q\nwant %q", c.args, got, c.want)
		}
	}
	// The mention flag may repeat.
	if got := cliError(t, "mail", "send", "--mention", "a", "--mention", "b", "--task", "x", "--state-dir", state); !strings.HasPrefix(got, "--task takes a Task ID") {
		t.Fatalf("repeated --mention: %q", got)
	}
	// A group word alone prints its commands, like a-stack help task.
	var out, help, errout bytes.Buffer
	if run([]string{"task"}, &out, &errout) != 0 || errout.Len() != 0 || run([]string{"help", "task"}, &help, &errout) != 0 || out.String() != help.String() || !strings.Contains(out.String(), "task attention set") {
		t.Fatalf("bare group word: %q %q", out.String(), errout.String())
	}
	out.Reset()
	if run([]string{"--version"}, &out, &errout) != 0 || out.String() != "{\"data\":{\"version\":\"dev\"}}\n" {
		t.Fatalf("--version: %q", out.String())
	}
}

func TestCLIBodyAliasAndOneTrailingNewline(t *testing.T) {
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
		_ = service.Close(ctx)
		<-done
	})
	project, private := filepath.Join(root, "project"), filepath.Join(root, "credentials")
	if os.Mkdir(project, 0700) != nil || stack.MkdirPrivate(private) != nil {
		t.Fatal("fixture directories")
	}
	var ws, se string
	call := func(credential string, args ...string) map[string]any {
		t.Helper()
		args = append(args, "--state-dir", dir)
		if credential != "" {
			args = append(args, "--as", "agent", "--credential-file", credential, "--workspace", ws, "--session", se)
		}
		var out, errout bytes.Buffer
		if code := run(args, &out, &errout); code != 0 {
			t.Fatalf("%v: exit %d %s", args, code, errout.String())
		}
		if !bytes.HasSuffix(out.Bytes(), []byte("}\n")) || bytes.HasSuffix(out.Bytes(), []byte("\n\n")) {
			t.Fatalf("%v: output must end in exactly one newline: %q", args[:2], out.String())
		}
		var envelope struct {
			Data map[string]any `json:"data"`
		}
		if json.Unmarshal(out.Bytes(), &envelope) != nil {
			t.Fatal(out.String())
		}
		return envelope.Data
	}
	ws = call("", "workspace", "create", "--path", project, "--operation-id", "alias-ws-1")["resource_id"].(string)
	se = call("", "session", "create", "--workspace", ws, "--name", "Alias", "--operation-id", "alias-se-1")["resource_id"].(string)
	orch := filepath.Join(private, "orch.json")
	orchID := call("", "agent", "register", "--workspace", ws, "--session", se, "--name", "01-orch", "--new-credential-file", orch, "--operation-id", "alias-reg-1")["resource_id"].(string)
	call("", "session", "appoint", "--workspace", ws, "--session", se, "--agent", orchID, "--expected-revision", "1", "--operation-id", "alias-app-1")
	task := call(orch, "task", "create", "--title", "T", "--body", "described", "--operation-id", "alias-task-1")["resource_id"].(string)
	call(orch, "task", "assign", "--task", task, "--agent", orchID, "--expected-revision", "1", "--operation-id", "alias-assign-1")
	call(orch, "task", "status", "--task", task, "--progress", "waiting", "--body", "Waits for review", "--expected-revision", "2", "--operation-id", "alias-wait-1")
	call(orch, "task", "report", "--task", task, "--body", "Reported", "--expected-revision", "3", "--operation-id", "alias-report-1")
	call(orch, "task", "attention", "set", "--task", task, "--body", "Which?", "--expected-revision", "4", "--operation-id", "alias-ask-1")
	call(orch, "task", "attention", "resolve", "--task", task, "--body", "That one", "--expected-revision", "5", "--operation-id", "alias-answer-1")
	call(orch, "task", "status", "--task", task, "--progress", "finished", "--body", "Done", "--expected-revision", "6", "--operation-id", "alias-finish-1")
	call(orch, "task", "reopen", "--task", task, "--body", "Again", "--expected-revision", "7", "--operation-id", "alias-reopen-1")
	call(orch, "task", "edit", "--task", task, "--body", "Edited", "--expected-revision", "8", "--operation-id", "alias-edit-1")
	got := call(orch, "task", "show", "--task", task)
	if got["description"] != "Edited" || got["report"] != "Reported" || got["reason"] != "Again" || got["progress"] != "waiting" {
		t.Fatalf("--body did not reach the text fields: %v", got)
	}
}
