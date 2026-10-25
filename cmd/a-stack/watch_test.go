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
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// team is a running in-process service with one Session, an Orchestrator and
// a worker with one assigned Task.
type team struct {
	t                                      *testing.T
	state, project, orchFile, workerFile   string
	workspace, session, worker, orch, task string
}

func newTeam(t *testing.T) *team {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	f := &team{t: t, state: filepath.Join(root, "state"), project: filepath.Join(root, "project")}
	service, err := stack.Start(ctx, f.state, "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- service.Serve() }()
	t.Cleanup(func() {
		_ = service.Close(ctx)
		<-done
	})
	private := filepath.Join(root, "credentials")
	if os.Mkdir(f.project, 0700) != nil || os.Mkdir(private, 0700) != nil {
		t.Fatal("fixture directories")
	}
	f.project, _ = filepath.EvalSymlinks(f.project)
	f.orchFile, f.workerFile = filepath.Join(private, "orch.json"), filepath.Join(private, "worker.json")
	f.workspace = f.receipt("", "workspace", "create", "--path", f.project, "--operation-id", "team-workspace").ResourceID
	f.session = f.receipt("", "session", "create", "--workspace", f.workspace, "--name", "Build", "--operation-id", "team-session").ResourceID
	f.orch = f.receipt("", f.scoped("agent", "register", "--name", "01-orchestrator", "--new-credential-file", f.orchFile, "--operation-id", "team-orch")...).ResourceID
	f.receipt("", f.scoped("session", "appoint", "--agent", f.orch, "--expected-revision", "1", "--operation-id", "team-appoint")...)
	f.worker = f.receipt(f.orchFile, f.scoped("agent", "register", "--name", "02-worker", "--new-credential-file", f.workerFile, "--operation-id", "team-worker")...).ResourceID
	f.task = f.receipt(f.orchFile, f.scoped("task", "create", "--title", "Build the parser", "--operation-id", "team-task")...).ResourceID
	f.receipt(f.orchFile, f.scoped("task", "assign", "--task", f.task, "--agent", f.worker, "--expected-revision", "1", "--operation-id", "team-assign")...)
	return f
}

func (f *team) scoped(args ...string) []string {
	return append(args, "--workspace", f.workspace, "--session", f.session)
}

// run runs the CLI as the Operator (credential "") or as an Agent.
func (f *team) run(credential string, args ...string) (string, string, int) {
	f.t.Helper()
	args = append(args, "--state-dir", f.state)
	if credential != "" {
		args = append(args, "--as", "agent", "--credential-file", credential)
	}
	var out, errout bytes.Buffer
	code := run(args, &out, &errout)
	return out.String(), errout.String(), code
}

func (f *team) receipt(credential string, args ...string) stack.Receipt {
	f.t.Helper()
	out, errout, code := f.run(credential, append(args, "--json")...)
	var envelope struct {
		Data stack.Receipt `json:"data"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &envelope) != nil {
		f.t.Fatalf("%v: exit %d %s", args[:2], code, errout)
	}
	return envelope.Data
}

func (f *team) taskChange(credential string, id string, revision int64, args ...string) int64 {
	f.t.Helper()
	all := append([]string{"task"}, args...)
	all = append(all, "--task", f.task, "--expected-revision", strconv.FormatInt(revision, 10), "--operation-id", id)
	return f.receipt(credential, f.scoped(all...)...).Revision
}

// syncBuffer is a bytes.Buffer that watch writes while the test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func waitFor(t *testing.T, what string, out *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("%s: no %q in:\n%s", what, want, out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStatusCountsTheCurrentFolder(t *testing.T) {
	f := newTeam(t)
	elsewhere := t.TempDir()
	t.Chdir(elsewhere)
	if _, errout, code := f.run("", "status"); code != 2 || !strings.Contains(errout, "this folder has no Workspace") {
		t.Fatalf("status outside a Workspace: %d %s", code, errout)
	}
	if err := os.Mkdir(filepath.Join(f.project, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Join(f.project, "sub"))
	rev := f.taskChange(f.workerFile, "status-working", 2, "status", "--progress", "working")
	f.receipt(f.orchFile, f.scoped("task", "create", "--title", "Waits", "--operation-id", "status-waits")...)
	out, _, code := f.run("", "status")
	if code != 0 || !strings.HasPrefix(out, `"Build" in `) || !strings.HasSuffix(out, ": 0 needs you · 1 working · 1 waiting · 0 finished\n") {
		t.Fatalf("status: %d %q", code, out)
	}
	f.taskChange(f.workerFile, "status-ask", rev, "attention", "set", "--question", "Which parser?")
	out, _, _ = f.run("", "status", "--json")
	var envelope struct {
		Data struct {
			Sessions []sessionStatus `json:"sessions"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(out), &envelope) != nil || len(envelope.Data.Sessions) != 1 {
		t.Fatalf("status --json: %s", out)
	}
	if s := envelope.Data.Sessions[0]; s.SessionID != f.session || s.NeedsYou != 1 || s.Working != 0 || s.Waiting != 1 {
		t.Fatalf("status counts: %+v", s)
	}
	// --all and --session work from any folder; a closed Session counts only
	// when --session names it.
	t.Chdir(elsewhere)
	f.receipt("", f.scoped("session", "close", "--expected-revision", "2", "--operation-id", "status-close")...)
	if out, _, _ = f.run("", "status", "--all"); out != "No open Session here. To create one, run: a-stack new\n" {
		t.Fatalf("status --all with a closed Session: %q", out)
	}
	if out, _, _ = f.run("", "status", "--session", f.session); !strings.Contains(out, "(closed): 1 needs you") {
		t.Fatalf("status --session: %q", out)
	}
	if _, errout, code := f.run("", "status", "--all", "--session", f.session); code != 2 || !strings.Contains(errout, "--all or --session") {
		t.Fatalf("both scopes: %s", errout)
	}
}

func TestWatchPrintsChangesAndRings(t *testing.T) {
	f := newTeam(t)
	t.Chdir(f.project)
	client, err := stack.NewClient(f.state, "", stack.OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}
	var out, errout syncBuffer
	var mu sync.Mutex
	notices := []string{}
	w := &watcher{client: client, notify: true, stdout: &out, stderr: &errout, interval: 20 * time.Millisecond, notifier: func(summary, body string) error {
		mu.Lock()
		defer mu.Unlock()
		notices = append(notices, summary+"|"+body)
		return nil
	}}
	w.setup()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.watch(ctx) }()
	waitFor(t, "start", &out, "0 needs you · 0 working · 1 waiting · 0 finished\n")
	if !strings.HasPrefix(out.String(), "Watching the Sessions of ") {
		t.Fatalf("watch header: %q", out.String())
	}
	rev := f.taskChange(f.workerFile, "watch-working", 2, "status", "--progress", "working")
	waitFor(t, "progress", &out, "  Build  Build the parser: waiting → working\n")
	rev = f.taskChange(f.workerFile, "watch-ask", rev, "attention", "set", "--question", "Which <b>parser</b>?")
	waitFor(t, "needs you", &out, "Build the parser: needs you: Which <b>parser</b>?\n\a")
	rev = f.taskChange(f.workerFile, "watch-answer", rev, "attention", "resolve", "--note", "The fast one")
	waitFor(t, "answered", &out, "Build the parser: Needs you answered\n")
	rev = f.taskChange(f.workerFile, "watch-report", rev, "report", "--report", "Parser done")
	waitFor(t, "report", &out, "Build the parser: new report: Parser done\n")
	f.taskChange(f.workerFile, "watch-finish", rev, "status", "--progress", "finished", "--summary", "Done")
	waitFor(t, "finish", &out, "Build the parser: working → finished\n")
	f.receipt(f.orchFile, f.scoped("task", "create", "--title", "Next \x1b[31mstep", "--operation-id", "watch-next")...)
	waitFor(t, "new Task", &out, "Next �[31mstep: new Task, waiting\n")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notices) != 1 || notices[0] != "a-stack: 1 needs you in Build|" {
		t.Fatalf("notices without --notify-text: %q", notices)
	}
	if strings.Count(out.String(), "\a") != 1 || errout.String() != "" {
		t.Fatalf("bell or diagnostics: %q %q", out.String(), errout.String())
	}
}

func TestWatchSkipsUnchangedSessionsAndNotifiesText(t *testing.T) {
	f := newTeam(t)
	client, err := stack.NewClient(f.state, "", stack.OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}
	var out, errout syncBuffer
	notices := []string{}
	w := &watcher{client: client, session: f.session, machine: true, notify: true, notifyText: true, stdout: &out, stderr: &errout, notifier: func(summary, body string) error {
		notices = append(notices, summary+"|"+body)
		return nil
	}}
	w.setup()
	if _, err := w.poll(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	known := w.seen[f.session]
	known.tasks[f.task] = stack.Task{ID: "marker"}
	if _, err := w.poll(context.Background(), false); err != nil || w.seen[f.session].tasks[f.task].ID != "marker" || out.String() != "" {
		t.Fatalf("an unchanged Session was read again: %v %q", err, out.String())
	}
	delete(known.tasks, f.task)
	f.taskChange(f.workerFile, "skip-ask", 2, "attention", "set", "--question", "A & B?")
	if _, err := w.poll(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var first, second struct {
		Data watchEvent `json:"data"`
	}
	if len(lines) != 2 || json.Unmarshal([]byte(lines[0]), &first) != nil || json.Unmarshal([]byte(lines[1]), &second) != nil || first.Data.Change != "created" || second.Data.Change != "needs_you" || second.Data.Text != "A & B?" || strings.Contains(out.String(), "\a") {
		t.Fatalf("JSON events: %q", out.String())
	}
	if len(notices) != 1 || notices[0] != "a-stack: 1 needs you in Build|A &amp; B?" {
		t.Fatalf("notice with text: %q", notices)
	}
}

func TestStatusLineForClaudeCode(t *testing.T) {
	f := newTeam(t)
	line := func(state, input string) string {
		var out bytes.Buffer
		statusLine(state, strings.NewReader(input), &out)
		return out.String()
	}
	input := `{"cwd":"/elsewhere","workspace":{"current_dir":` + jsonString(f.project) + `}}`
	if got := line(f.state, input); got != "a-stack Build: 0 needs you · 0 working\n" {
		t.Fatalf("status line %q", got)
	}
	rev := f.taskChange(f.workerFile, "line-working", 2, "status", "--progress", "working")
	f.taskChange(f.workerFile, "line-ask", rev, "attention", "set", "--question", "Which?")
	f.receipt("", "session", "create", "--workspace", f.workspace, "--name", "Second", "--operation-id", "line-second")
	if got := line(f.state, `{"cwd":`+jsonString(f.project)+`}`); got != "a-stack: 1 needs you · 0 working\n" {
		t.Fatalf("status line of two Sessions %q", got)
	}
	for _, quiet := range []string{line(f.state, `{"cwd":"/"}`), line(f.state, "nonsense"), line(filepath.Join(t.TempDir(), "none"), input)} {
		if quiet != "" {
			t.Fatalf("status line printed %q", quiet)
		}
	}
	var out, errout bytes.Buffer
	if code := run([]string{"statusline", "--no-such-flag"}, &out, &errout); code != 0 || out.Len() != 0 || errout.Len() != 0 {
		t.Fatalf("statusline error: %d %q %q", code, out.String(), errout.String())
	}
}
