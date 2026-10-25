package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// hookHome gives the test an empty home folder with no harness overrides.
func hookHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	return home
}

type settingsFile struct {
	Hooks map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Timeout int    `json:"timeout"`
		} `json:"hooks"`
	} `json:"hooks"`
	Other json.RawMessage `json:"model"`
}

func readSettings(t *testing.T, file string) settingsFile {
	t.Helper()
	var s settingsFile
	data, err := os.ReadFile(file)
	if err != nil || json.Unmarshal(data, &s) != nil {
		t.Fatalf("settings %s: %v %s", file, err, data)
	}
	return s
}

// commands lists the commands of one event in a settings file.
func commands(s settingsFile, event string) []string {
	out := []string{}
	for _, g := range s.Hooks[event] {
		for _, h := range g.Hooks {
			out = append(out, h.Command)
		}
	}
	return out
}

func TestHookInstallMergesAndUninstallRemovesOnlyItsOwn(t *testing.T) {
	f := newTeam(t)
	home := hookHome(t)
	claude := filepath.Join(home, ".claude", "settings.json")
	codex := filepath.Join(home, ".codex", "hooks.json")
	if os.MkdirAll(filepath.Dir(claude), 0700) != nil {
		t.Fatal("fixture")
	}
	before := `{"model":"opus","hooks":{"Stop":[{"hooks":[{"type":"command","command":"say done"}]}],"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"guard.sh"}]}]}}`
	if os.WriteFile(claude, []byte(before), 0644) != nil {
		t.Fatal("fixture")
	}
	t.Chdir(f.project)
	install := []string{"hook", "install", "--credential-file", f.workerFile, "--workspace", f.workspace, "--session", f.session, "--name", "02-worker", "--runtime-id", "it's-me"}
	out, errout, code := f.run("", install...)
	if code != 0 || !strings.Contains(out, "Installed the a-stack hooks for "+f.project+" in:\n  "+claude+"\n  "+codex+"\n") || !strings.Contains(out, "run /hooks in Codex") {
		t.Fatalf("install: %d %q %s", code, out, errout)
	}
	s := readSettings(t, claude)
	if string(s.Other) != `"opus"` || len(commands(s, "PreToolUse")) != 1 || commands(s, "Stop")[0] != "say done" {
		t.Fatalf("other settings lost: %+v", s)
	}
	ours := commands(s, "Stop")[1]
	exe, _ := os.Executable()
	want := shellQuote(exe) + " hook --state-dir " + shellQuote(f.state) + " --credential-file " + shellQuote(f.workerFile) + " --workspace " + f.workspace + " --session " + f.session + " --agent " + f.worker + ` --runtime-id 'it'\''s-me' --worktree ` + shellQuote(f.project)
	if ours != want {
		t.Fatalf("hook command\n got %s\nwant %s", ours, want)
	}
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "PermissionRequest", "Notification"} {
		if c := commands(s, event); len(c) != 1 || c[0] != want {
			t.Fatalf("%s: %v", event, c)
		}
	}
	if g := s.Hooks["Notification"][0]; g.Matcher != "elicitation_dialog" || g.Hooks[0].Timeout != hookTimeoutSeconds || g.Hooks[0].Type != "command" {
		t.Fatalf("Notification group %+v", g)
	}
	c := readSettings(t, codex)
	if _, ok := c.Hooks["Notification"]; ok || len(c.Hooks) != 5 || commands(c, "SessionStart")[0] != want {
		t.Fatalf("Codex hooks: %+v", c.Hooks)
	}
	if info, err := os.Stat(claude); err != nil || info.Mode().Perm() != 0644 {
		t.Fatal("install changed the file mode")
	}
	if info, err := os.Stat(codex); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("new hooks file is not private")
	}

	// Installing again replaces the entries; another worktree's stay.
	other := t.TempDir()
	if _, errout, code = f.run("", append(install, "--worktree", other, "--claude")...); code != 0 {
		t.Fatalf("second worktree: %s", errout)
	}
	if out, _, _ = f.run("", install...); !strings.Contains(out, "Installed") {
		t.Fatalf("reinstall: %q", out)
	}
	if c := commands(readSettings(t, claude), "Stop"); len(c) != 3 || c[0] != "say done" || !strings.HasSuffix(c[1], shellQuote(other)) || c[2] != want {
		t.Fatalf("after reinstall: %v", c)
	}
	if out = runSkill(t, "hook", "uninstall"); !strings.Contains(out, "Removed the a-stack hooks for "+f.project+" from:\n  "+claude+"\n  "+codex+"\n") {
		t.Fatalf("uninstall: %q", out)
	}
	s = readSettings(t, claude)
	if c := commands(s, "Stop"); len(c) != 2 || c[0] != "say done" || len(s.Hooks) != 7 || len(commands(s, "PreToolUse")) != 1 {
		t.Fatalf("uninstall removed other hooks: %+v", s.Hooks)
	}
	if data, _ := os.ReadFile(codex); string(data) != "{}\n" {
		t.Fatalf("Codex file after uninstall: %s", data)
	}
	if out = runSkill(t, "hook", "uninstall", "--codex"); out != "No a-stack hooks for "+f.project+" were installed.\n" {
		t.Fatalf("second uninstall: %q", out)
	}

	// A linked settings file stays a link; a broken one is not overwritten.
	real := filepath.Join(home, "dotfiles.json")
	if os.Rename(claude, real) != nil || os.Symlink(real, claude) != nil {
		t.Fatal("link fixture")
	}
	if _, errout, code = f.run("", append(install, "--claude")...); code != 0 {
		t.Fatalf("install through a link: %s", errout)
	}
	if target, err := os.Readlink(claude); err != nil || target != real || len(commands(readSettings(t, real), "Stop")) != 3 {
		t.Fatal("the link was replaced")
	}
	if os.WriteFile(codex, []byte("{not json"), 0600) != nil {
		t.Fatal("fixture")
	}
	if _, errout, code = f.run("", append(install, "--codex")...); code != 1 || !strings.Contains(errout, "not a JSON object") {
		t.Fatalf("broken file: %d %s", code, errout)
	}
	if data, _ := os.ReadFile(codex); string(data) != "{not json" {
		t.Fatal("broken file was overwritten")
	}
	for _, bad := range [][]string{
		{"hook", "install", "--credential-file", "relative.json", "--workspace", f.workspace, "--session", f.session, "--agent", f.worker},
		{"hook", "install", "--credential-file", f.workerFile, "--workspace", f.workspace, "--session", f.session},
		{"hook", "install", "--credential-file", filepath.Join(f.state, "operator.json"), "--workspace", f.workspace, "--session", f.session, "--agent", f.worker},
		{"hook", "install", "--credential-file", f.workerFile, "--workspace", f.workspace, "--session", f.session, "--name", "nobody"},
	} {
		if _, errout, code := f.run("", bad...); code == 0 || !json.Valid([]byte(errout)) {
			t.Fatalf("%v: %d %s", bad, code, errout)
		}
	}
}

func hookEvent(t *testing.T, id hookIdentity, event string, extra string) string {
	t.Helper()
	var out bytes.Buffer
	input := `{"session_id":"abc","cwd":` + jsonString(id.worktree) + `,"hook_event_name":"` + event + `"` + extra + `}`
	runHook(id, strings.NewReader(input), &out)
	return out.String()
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func runtimeState(t *testing.T, f *team) string {
	t.Helper()
	out, _, _ := f.run("", f.scoped("agent", "list", "--json")...)
	var envelope struct {
		Data stack.Page[stack.Agent] `json:"data"`
	}
	if json.Unmarshal([]byte(out), &envelope) != nil {
		t.Fatalf("agent list %s", out)
	}
	for _, a := range envelope.Data.Items {
		if a.ID == f.worker {
			return a.RuntimeState
		}
	}
	return ""
}

func TestHookRecordsRuntimeStateAndPrintsTheReminder(t *testing.T) {
	f := newTeam(t)
	f.receipt(f.workerFile, f.scoped("agent", "attach", "--agent", f.worker, "--runtime-id", "worker-runtime", "--expected-revision", "1", "--operation-id", "hook-attach")...)
	rev := f.taskChange(f.workerFile, "hook-working", 2, "status", "--progress", "working")
	f.taskChange(f.workerFile, "hook-ask", rev, "attention", "set", "--question", "Which?")
	done := f.receipt(f.orchFile, f.scoped("task", "create", "--title", "Old work", "--operation-id", "hook-old")...)
	f.receipt(f.orchFile, f.scoped("task", "assign", "--task", done.ResourceID, "--agent", f.worker, "--expected-revision", "1", "--operation-id", "hook-old-assign")...)
	f.receipt(f.workerFile, f.scoped("task", "status", "--task", done.ResourceID, "--progress", "finished", "--summary", "Done", "--expected-revision", "2", "--operation-id", "hook-old-finish")...)
	f.receipt(f.orchFile, f.scoped("task", "create", "--title", "Not mine", "--operation-id", "hook-not-mine")...)
	id := hookIdentity{state: f.state, credential: f.workerFile, agent: f.worker, runtimeID: "worker-runtime", worktree: f.project, ref: stack.Context{WorkspaceID: f.workspace, SessionID: f.session}}

	text := hookEvent(t, id, "SessionStart", `,"source":"compact"`)
	for _, want := range []string{"a-stack: you are the Agent 02-worker in the a-stack Session \"Build\"", "--credential-file " + shellQuote(f.workerFile) + " --workspace " + f.workspace, "Your open Tasks:\n- Build the parser (Task " + f.task + "): working, waits for the Operator's answer\n", "Run mail check now"} {
		if !strings.Contains(text, want) {
			t.Fatalf("reminder misses %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Old work") || strings.Contains(text, "Not mine") {
		t.Fatalf("reminder lists other Tasks:\n%s", text)
	}
	for _, step := range []struct{ event, extra, want string }{
		{"UserPromptSubmit", `,"prompt":"go"`, "working"},
		{"PermissionRequest", `,"tool_name":"Bash"`, "blocked"},
		{"PostToolUse", `,"tool_response":{"stdout":"` + strings.Repeat("x", 1<<20) + `"}`, "working"},
		{"Notification", `,"notification_type":"idle_prompt"`, "working"},
		{"Notification", `,"notification_type":"elicitation_dialog"`, "blocked"},
		{"Stop", `,"stop_hook_active":false`, "idle"},
		{"Stop", "", "idle"},
	} {
		if out := hookEvent(t, id, step.event, step.extra); out != "" {
			t.Fatalf("%s printed %q", step.event, out)
		}
		if got := runtimeState(t, f); got != step.want {
			t.Fatalf("after %s: %s, want %s", step.event, got, step.want)
		}
	}
	// Another folder, another runtime or no runtime ID changes nothing.
	elsewhere := id
	elsewhere.worktree = t.TempDir()
	runHook(elsewhere, strings.NewReader(`{"cwd":"`+f.project+`","hook_event_name":"UserPromptSubmit"}`), &bytes.Buffer{})
	stranger := id
	stranger.runtimeID = "someone-else"
	hookEvent(t, stranger, "UserPromptSubmit", "")
	unbound := id
	unbound.runtimeID = ""
	hookEvent(t, unbound, "UserPromptSubmit", "")
	if got := runtimeState(t, f); got != "idle" {
		t.Fatalf("a foreign hook changed the state to %s", got)
	}
	var out bytes.Buffer
	runHook(id, strings.NewReader("not json"), &out)
	if out.Len() != 0 {
		t.Fatal("bad input printed")
	}
}

func TestHookExitsZeroQuicklyWithoutService(t *testing.T) {
	dir := t.TempDir()
	credential := filepath.Join(dir, "none.json")
	command := child(t, "hook", "--state-dir", filepath.Join(dir, "state"), "--credential-file", credential, "--workspace", "x", "--session", "y", "--agent", "z", "--runtime-id", "r", "--no-such-flag")
	command.Stdin = strings.NewReader(`{"hook_event_name":"SessionStart","cwd":"/"}`)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	start := time.Now()
	if err := command.Run(); err != nil || stdout.Len() != 0 || stderr.Len() != 0 || time.Since(start) > 5*time.Second {
		t.Fatalf("bad flags: %v %q %q", err, stdout.String(), stderr.String())
	}
	for _, event := range []string{"SessionStart", "Stop"} {
		command = child(t, "hook", "--state-dir", filepath.Join(dir, "state"), "--credential-file", credential, "--workspace", "x", "--session", "y", "--agent", "z", "--runtime-id", "r")
		command.Stdin = strings.NewReader(`{"hook_event_name":"` + event + `","cwd":"/"}`)
		command.Stdout, command.Stderr = &stdout, &stderr
		if err := command.Run(); err != nil || stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("%s without service: %v %q %q", event, err, stdout.String(), stderr.String())
		}
	}
}
