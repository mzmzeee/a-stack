package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
)

type shortcutResult struct {
	URL                string `json:"url"`
	Hint               string `json:"hint"`
	WorkspaceID        string `json:"workspace_id"`
	WorkspacePath      string `json:"workspace_path"`
	SessionID          string `json:"session_id"`
	SessionName        string `json:"session_name"`
	CurrentFolder      bool   `json:"current_folder"`
	OtherOpenSessions  *int   `json:"other_open_sessions"`
	WorkspaceOperation string `json:"workspace_operation_id"`
	SessionOperation   string `json:"session_operation_id"`
}

// shortcut runs an Operator shortcut in this process, with a fake opener.
func shortcut(t *testing.T, args ...string) (shortcutResult, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("%v: exit %d stderr=%s", args, code, stderr.String())
	}
	var envelope struct {
		Data shortcutResult `json:"data"`
	}
	if strings.Contains(strings.Join(args, " "), "--json") && json.Unmarshal(stdout.Bytes(), &envelope) != nil {
		t.Fatalf("%v: stdout is not one JSON document: %q", args, stdout.String())
	}
	return envelope.Data, stdout.String(), stderr.String()
}

var operationIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

func TestCLIShortcutsWithRunningService(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	launch(t, dir)
	old := launchBrowser
	t.Cleanup(func() { launchBrowser = old })
	var launched []string
	launchBrowser = func(u string) error {
		launched = append(launched, u)
		return errors.New("simulated opener failure")
	}
	alpha, beta, elsewhere := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(alpha, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(elsewhere)
	got, out, _ := shortcut(t, "--state-dir", dir, "--json")
	if got.Hint == "" || !strings.Contains(got.Hint, "a-stack new") || got.URL != "" || len(launched) != 0 {
		t.Fatalf("no Session hint: %q", out)
	}
	_, out, _ = shortcut(t, "--state-dir", dir)
	if !strings.HasPrefix(out, "No Session yet.") || strings.Count(out, "\n") != 1 {
		t.Fatalf("plain hint %q", out)
	}

	t.Chdir(alpha)
	first, _, _ := shortcut(t, "new", "--state-dir", dir, "--json", "--no-browser")
	canonicalAlpha, _ := filepath.EvalSymlinks(alpha)
	if first.WorkspacePath != canonicalAlpha || first.SessionName != filepath.Base(canonicalAlpha) || !first.CurrentFolder || !operationIDPattern.MatchString(first.WorkspaceOperation) || !operationIDPattern.MatchString(first.SessionOperation) || first.WorkspaceOperation == first.SessionOperation || first.URL == "" || len(launched) != 0 {
		t.Fatalf("new did not create Workspace and Session: %+v", first)
	}
	second, _, _ := shortcut(t, "new", "Second build", "--state-dir", dir, "--json", "--no-browser")
	if second.WorkspaceID != first.WorkspaceID || second.WorkspaceOperation != "" || second.SessionName != "Second build" || second.SessionID == first.SessionID {
		t.Fatalf("new did not reuse the Workspace: %+v", second)
	}
	t.Chdir(beta)
	_, out, _ = shortcut(t, "new", "--state-dir", dir, "--no-browser")
	if !strings.HasPrefix(out, "Created the Workspace ") || !strings.Contains(out, "Created the Session ") || !strings.Contains(out, "(current folder).\nhttp://") {
		t.Fatalf("plain new output %q", out)
	}

	// The newest open Session overall is in beta. A subfolder of alpha still
	// prefers alpha's newest Session.
	t.Chdir(elsewhere)
	got, _, _ = shortcut(t, "--state-dir", dir, "--json", "--no-browser")
	if got.WorkspacePath == canonicalAlpha || got.CurrentFolder || got.OtherOpenSessions == nil || *got.OtherOpenSessions != 2 {
		t.Fatalf("bare did not open the newest Session: %+v", got)
	}
	t.Chdir(filepath.Join(alpha, "sub"))
	got, _, _ = shortcut(t, "--state-dir", dir, "--json", "--no-browser")
	if got.SessionID != second.SessionID || !got.CurrentFolder {
		t.Fatalf("bare did not prefer the current folder: %+v", got)
	}
	// A change to the older alpha Session makes it the most recent one.
	common := func(args ...string) []string { return append(args, "--state-dir", dir, "--json") }
	receiptResult(t, common("session", "close", "--workspace", first.WorkspaceID, "--session", second.SessionID, "--expected-revision", "1", "--operation-id", "home-close-second"))
	receiptResult(t, common("session", "close", "--workspace", first.WorkspaceID, "--session", first.SessionID, "--expected-revision", "1", "--operation-id", "home-close-first"))
	receiptResult(t, common("session", "reopen", "--workspace", first.WorkspaceID, "--session", first.SessionID, "--expected-revision", "2", "--operation-id", "home-reopen-first"))
	t.Chdir(elsewhere)
	_, out, errout := shortcut(t, "--state-dir", dir)
	u, err := url.Parse(strings.Split(strings.TrimSpace(out), "\n")[1])
	if err != nil || len(u.Fragment) != 43 || !strings.Contains(out, "Opening the Session \""+first.SessionName+"\"") || !strings.Contains(out, "Other open Sessions: 1. To pick one, run: a-stack -r") {
		t.Fatalf("bare did not open the most recently changed Session: %q", out)
	}
	if len(launched) != 1 || launched[0] != u.String() || errout != "Browser launch failed; open the printed URL.\n" || strings.Contains(errout, u.Fragment) {
		t.Fatal("bare did not launch the URL safely")
	}

	// -r without a terminal or with --json lists all Sessions, closed ones too.
	t.Chdir(alpha)
	var stdout, stderr bytes.Buffer
	if run([]string{"-r", "--state-dir", dir, "--json"}, &stdout, &stderr) != 0 || stderr.Len() != 0 {
		t.Fatalf("resume list: %s", stderr.String())
	}
	var list struct {
		Data struct {
			Folder   string       `json:"folder"`
			Sessions []sessionRow `json:"sessions"`
		} `json:"data"`
	}
	if json.Unmarshal(stdout.Bytes(), &list) != nil || list.Data.Folder != canonicalAlpha || len(list.Data.Sessions) != 3 || list.Data.Sessions[0].SessionID != first.SessionID || !list.Data.Sessions[0].CurrentFolder || list.Data.Sessions[1].SessionID != second.SessionID || !list.Data.Sessions[1].Closed || bytes.Contains(stdout.Bytes(), []byte("#")) {
		t.Fatalf("resume list %s", stdout.String())
	}
	if len(launched) != 1 {
		t.Fatal("resume list opened a view")
	}
}

func TestCLIBareStartsServiceAndKeepsItRunning(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	t.Chdir(t.TempDir())
	command := child(t, "--state-dir", dir, "--json", "--no-browser")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	reader := bufio.NewReader(stdout)
	ready, err := reader.ReadBytes('\n')
	if err != nil || !json.Valid(ready) || !bytes.Contains(ready, []byte(`"status":"ready"`)) {
		t.Fatalf("not ready: %s %v", ready, err)
	}
	hint, err := reader.ReadBytes('\n')
	if err != nil || !bytes.Contains(hint, []byte(`"hint"`)) {
		t.Fatalf("no hint: %s %v", hint, err)
	}
	// The hint did not stop the service: a second shortcut is a client of it.
	created, _ := commandResult(t, []string{"new", "Started", "--state-dir", dir, "--json", "--no-browser"}, 0)
	opened, _ := commandResult(t, []string{"--state-dir", dir, "--json", "--no-browser"}, 0)
	var a, b struct {
		Data shortcutResult `json:"data"`
	}
	if json.Unmarshal(created, &a) != nil || json.Unmarshal(opened, &b) != nil || b.Data.SessionID != a.Data.SessionID || b.Data.SessionName != "Started" {
		t.Fatalf("second shortcut did not use the started service: %s %s", created, opened)
	}
	commandResult(t, []string{"serve", "--state-dir", dir, "--json"}, 1)
	if command.Process.Signal(syscall.SIGTERM) != nil {
		t.Fatal("terminate")
	}
	if err = command.Wait(); err != nil || stderr.Len() != 0 {
		t.Fatalf("shutdown: %v %s", err, stderr.String())
	}
	commandResult(t, []string{"health", "--state-dir", dir, "--json"}, 1)
	// With a Session, a start opens it after the ready line, and the URL
	// fragment appears only on stdout.
	command = child(t, "--state-dir", dir, "--no-browser")
	stdout, err = command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	command.Stderr = &stderr
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	reader = bufio.NewReader(stdout)
	lines := []string{}
	for len(lines) < 3 {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("output %q: %v", lines, err)
		}
		lines = append(lines, line)
	}
	u, err := url.Parse(strings.TrimSpace(lines[2]))
	if !strings.HasPrefix(lines[0], "The a-stack service is running at http://127.0.0.1:") || !strings.Contains(lines[1], `"Started"`) || err != nil || len(u.Fragment) != 43 {
		t.Fatalf("started open output %q", lines)
	}
	if command.Process.Signal(syscall.SIGINT) != nil {
		t.Fatal("interrupt")
	}
	if err = command.Wait(); err != nil || stderr.Len() != 0 {
		t.Fatalf("shutdown: %v %s", err, stderr.String())
	}
}

func TestCLIShortcutParsing(t *testing.T) {
	for _, args := range [][]string{{"--credential-file", "/x"}, {"--as", "operator"}, {"--listen", "127.0.0.1:0"}, {"new", "a", "b"}, {"new", "--resume"}, {"session", "list", "-r"}, {"open", "--resume"}, {"unsupported"}} {
		var out, errout bytes.Buffer
		if run(args, &out, &errout) != 2 || out.Len() != 0 || !json.Valid(errout.Bytes()) {
			t.Fatalf("bad machine error for %s", strings.Join(args, " "))
		}
	}
}
