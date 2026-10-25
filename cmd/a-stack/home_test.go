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

	"github.com/mzmzeee/a-stack/internal/stack"
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
	ExistingSession    bool   `json:"existing_session"`
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
	// The same name again opens the open Session of that name.
	again, _, _ := shortcut(t, "new", "--state-dir", dir, "--json", "--no-browser")
	if again.SessionID != first.SessionID || !again.ExistingSession || again.SessionOperation != "" || first.ExistingSession {
		t.Fatalf("new made a second Session of the same name: %+v", again)
	}
	_, out, _ = shortcut(t, "new", "Second build", "--state-dir", dir, "--no-browser")
	if !strings.HasPrefix(out, "The Session \"Second build\" already exists in this folder. To make another one, run: a-stack new NAME\nOpening the Session \"Second build\"") {
		t.Fatalf("plain new of an existing name %q", out)
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
	// A second serve on the owned state directory exits 3 (systemd RestartPreventExitStatus).
	commandResult(t, []string{"serve", "--state-dir", dir, "--json"}, 3)
	stopServe(t, command, syscall.SIGTERM)
	if stderr.Len() != 0 {
		t.Fatalf("shutdown: %s", stderr.String())
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
	for len(lines) < 4 {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("output %q: %v", lines, err)
		}
		lines = append(lines, line)
	}
	u, err := url.Parse(strings.TrimSpace(lines[3]))
	if !strings.HasPrefix(lines[0], "The a-stack service is running at http://127.0.0.1:") || lines[1] != closeHint+"\n" || !strings.Contains(lines[2], `"Started"`) || err != nil || len(u.Fragment) != 43 {
		t.Fatalf("started open output %q", lines)
	}
	stopServe(t, command, syscall.SIGINT)
	if stderr.Len() != 0 {
		t.Fatalf("shutdown: %s", stderr.String())
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

func TestCLIShortcutsReportLockedStateWithoutService(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// This stands for a serve that holds the lock but is still starting or hangs.
	defer holdStateLock(t, filepath.Join(dir, "service.lock"))()
	t.Chdir(t.TempDir())
	for _, args := range [][]string{{}, {"-r"}, {"new"}} {
		var stdout, stderr bytes.Buffer
		code := run(append(args, "--state-dir", dir, "--json", "--no-browser"), &stdout, &stderr)
		var envelope struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		if code != 3 || stdout.Len() != 0 || json.Unmarshal(stderr.Bytes(), &envelope) != nil || envelope.Error.Code != "conflict" || !strings.Contains(envelope.Error.Message, "does not answer yet") || strings.Contains(envelope.Error.Message, "not running") {
			t.Fatalf("%v: exit %d stdout=%q stderr=%s", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestCLIStartedServiceStopsCleanlyOnHangup(t *testing.T) {
	unixOnly(t, "no SIGHUP")
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
	for i := 0; i < 2; i++ { // the ready line and the hint
		if _, err = reader.ReadBytes('\n'); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = os.Stat(filepath.Join(dir, "service.json")); err != nil {
		t.Fatal(err)
	}
	if command.Process.Signal(syscall.SIGHUP) != nil {
		t.Fatal("hangup")
	}
	if err = command.Wait(); err != nil || stderr.Len() != 0 {
		t.Fatalf("shutdown: %v %s", err, stderr.String())
	}
	if _, err = os.Stat(filepath.Join(dir, "service.json")); !os.IsNotExist(err) {
		t.Fatalf("service.json left behind: %v", err)
	}
}

func TestCLINewHandsOffToOrchestrator(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	launch(t, dir)
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)
	t.Chdir(t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run([]string{"new", "--orchestrator", "bad name", "--state-dir", dir, "--json", "--no-browser"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "--orchestrator takes an Agent name") {
		t.Fatalf("bad name: exit %d %s", code, stderr.String())
	}
	var made struct {
		Data struct {
			shortcutResult
			OrchestratorID string `json:"orchestrator_id"`
			Credential     string `json:"orchestrator_credential_file"`
			Brief          string `json:"brief"`
		} `json:"data"`
	}
	stdout.Reset()
	if code := run([]string{"new", "--orchestrator", "01-orch", "--state-dir", dir, "--json", "--no-browser"}, &stdout, &stderr); code != 0 || json.Unmarshal(stdout.Bytes(), &made) != nil {
		t.Fatalf("new --orchestrator: exit %d %s %s", code, stdout.String(), stderr.String())
	}
	got := made.Data
	if got.Credential != filepath.Join(root, "a-stack-agents", got.SessionID, "01-orch.json") || !stack.ValidID(got.OrchestratorID) || got.SessionOperation == "" {
		t.Fatalf("hand-off result %+v", got)
	}
	info, err := os.Stat(got.Credential)
	if err != nil || posixModes && info.Mode().Perm() != 0o600 {
		t.Fatalf("credential file %v %v", info, err)
	}
	credential, err := stack.ReadCredential(got.Credential)
	if err != nil || credential.Token == "" || strings.Contains(got.Brief, credential.Token) || strings.Contains(stdout.String(), credential.Token) {
		t.Fatal("the brief or the output leaks the credential")
	}
	for _, want := range []string{got.Credential, got.WorkspaceID, got.SessionID, "01-orch", "orchestrator reference"} {
		if !strings.Contains(got.Brief, want) {
			t.Fatalf("brief lacks %q: %s", want, got.Brief)
		}
	}
	// The new credential names its Session, which now has this Orchestrator.
	stdout.Reset()
	if code := run([]string{"session", "inspect", "--as", "agent", "--credential-file", got.Credential, "--state-dir", dir}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"orchestrator_id":"`+got.OrchestratorID+`"`) {
		t.Fatalf("inspect as the Orchestrator: %s %s", stdout.String(), stderr.String())
	}
	// The same hand-off again returns it, for example to an agent that lost it.
	var again struct {
		Data struct {
			OrchestratorID string `json:"orchestrator_id"`
			Existing       bool   `json:"existing_orchestrator"`
			Brief          string `json:"brief"`
		} `json:"data"`
	}
	stdout.Reset()
	if code := run([]string{"new", "--orchestrator", "01-orch", "--state-dir", dir, "--json", "--no-browser"}, &stdout, &stderr); code != 0 || json.Unmarshal(stdout.Bytes(), &again) != nil {
		t.Fatalf("hand-off again: exit %d %s %s", code, stdout.String(), stderr.String())
	}
	if again.Data.OrchestratorID != got.OrchestratorID || !again.Data.Existing || again.Data.Brief != got.Brief {
		t.Fatalf("hand-off again %+v", again.Data)
	}
	// With --no-browser an agent may be the caller: no view link, so no view secret.
	if got.URL != "" || strings.Contains(stdout.String(), `"url"`) || !strings.Contains(got.Hint, "a-stack") {
		t.Fatalf("hand-off made a view link: %+v", got)
	}
	// A credential file where v0.1.5 kept it also proves the hand-off.
	legacy := filepath.Join(root, "a-stack-agents", "01-orch.json")
	if err := os.Rename(got.Credential, legacy); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := run([]string{"new", "--orchestrator", "01-orch", "--state-dir", dir, "--json", "--no-browser"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"existing_orchestrator":true`) || !strings.Contains(stdout.String(), legacy) {
		t.Fatalf("hand-off from the v0.1.5 path: exit %d %s %s", code, stdout.String(), stderr.String())
	}
	// Another project gets its own credential file with the same Orchestrator
	// name. (Within one Workspace, Agent names stay unique.)
	var other struct {
		Data struct {
			shortcutResult
			Credential string `json:"orchestrator_credential_file"`
		} `json:"data"`
	}
	project := t.TempDir()
	t.Chdir(project)
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"new", "--orchestrator", "01-orch", "--state-dir", dir, "--json", "--no-browser"}, &stdout, &stderr); code != 0 || json.Unmarshal(stdout.Bytes(), &other) != nil {
		t.Fatalf("hand-off in another Session: exit %d %s %s", code, stdout.String(), stderr.String())
	}
	if other.Data.SessionID == got.SessionID || other.Data.Credential != filepath.Join(root, "a-stack-agents", other.Data.SessionID, "01-orch.json") {
		t.Fatalf("hand-off in another Session %+v", other.Data)
	}
	// The same Session with another name: it already has an Orchestrator.
	stderr.Reset()
	if code := run([]string{"new", "--orchestrator", "02-orch", "--state-dir", dir, "--json", "--no-browser"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "already has an Orchestrator") {
		t.Fatalf("second Orchestrator: exit %d %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, "a-stack-agents", other.Data.SessionID, "02-orch.json")); !os.IsNotExist(err) {
		t.Fatal("a refused hand-off left a credential file")
	}
	// Without a service, the hand-off does not start one in the foreground.
	stderr.Reset()
	if code := run([]string{"new", "--orchestrator", "01-orch", "--state-dir", filepath.Join(root, "idle"), "--json", "--no-browser"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "a-stack service install") {
		t.Fatalf("hand-off without a service: exit %d %s", code, stderr.String())
	}
}
