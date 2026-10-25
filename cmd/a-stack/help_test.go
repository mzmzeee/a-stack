package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func helpOutput(t *testing.T, args ...string) string {
	t.Helper()
	var out, errout bytes.Buffer
	if code := run(args, &out, &errout); code != 0 || errout.Len() != 0 {
		t.Fatalf("%v: exit %d stderr=%s", args, code, errout.String())
	}
	return out.String()
}

func allCommands() []string {
	commands := []string{"new", "open", "serve", "health", "version", "storage status", "storage backup", "notifications status", "notifications drain", "skill install", "skill uninstall", "update", "view revoke", "status", "watch", "hook install", "hook uninstall", "hook", "statusline"}
	return append(append(append(commands, lifecycleCommands...), taskCommands...), mailCommands...)
}

func checkWidth(t *testing.T, name, text string) {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if utf8.RuneCountInString(line) > 80 || strings.ContainsAny(line, "\t\x1b") {
			t.Fatalf("%s: line over 80 columns: %q", name, line)
		}
	}
}

func TestHelpTopLevelAndAliases(t *testing.T) {
	top := helpOutput(t, "--help")
	checkWidth(t, "--help", top)
	if !strings.HasPrefix(top, "Keep an eye on your AI coding agents.\n\nUsage: a-stack <command> [flags]\n") || !strings.Contains(top, "\nStart here:\n  (no command)  ") || !strings.Contains(top, "\n  -r, --resume  ") || !strings.HasSuffix(top, "Docs: https://github.com/mzmzeee/a-stack\n") {
		t.Fatalf("top help:\n%s", top)
	}
	for _, c := range allCommands() {
		if !strings.Contains(top, "\n  "+c+" ") && !strings.Contains(top, "\n  "+c+" [NAME] ") {
			t.Fatalf("top help misses %q", c)
		}
		if !knownCommand(c) {
			t.Fatalf("help has no row for %q", c)
		}
	}
	for _, args := range [][]string{{"-h"}, {"help"}, {"help", "--state-dir", "/x"}} {
		if helpOutput(t, args...) != top {
			t.Fatalf("%v differs from --help", args)
		}
	}
	if helpOutput(t, "--version") != helpOutput(t, "version") {
		t.Fatal("--version differs from version")
	}
	for _, args := range [][]string{{"help", "nope"}, {"nope", "--help"}, {"serve", "--version"}} {
		var out, errout bytes.Buffer
		if run(args, &out, &errout) != 2 || out.Len() != 0 || !json.Valid(errout.Bytes()) {
			t.Fatalf("bad machine error for %v", args)
		}
	}
}

func TestHelpPerCommandFollowsAcceptedFlags(t *testing.T) {
	for _, c := range allCommands() {
		words := strings.Fields(c)
		text := helpOutput(t, append([]string{"help"}, words...)...)
		if helpOutput(t, append(words, "--help")...) != text || helpOutput(t, append(words, "-h")...) != text {
			t.Fatalf("%s: help forms differ", c)
		}
		checkWidth(t, c, text)
		// An Agent credential file names the Workspace and the Session.
		if !strings.Contains(text, "\nUsage: a-stack "+c+" ") || strings.Contains(text, " VALUE") || strings.Contains(text, "Workspace ID (required)") || strings.Contains(text, "Session ID (required)") {
			t.Fatalf("%s help:\n%s", c, text)
		}
		for _, name := range strings.Fields(acceptedFlags(c)) {
			if name != "r" && !strings.Contains(text, "--"+name) {
				t.Fatalf("%s help misses --%s", c, name)
			}
		}
	}
	for command, names := range requiredFlags {
		for _, name := range strings.Fields(names) {
			if !strings.Contains(acceptedFlags(command), " "+name+" ") {
				t.Fatalf("%s: required flag --%s is not accepted", command, name)
			}
		}
	}
	create := helpOutput(t, "task", "create", "--help")
	for _, want := range []string{"Create a Task.\n", "  --title TEXT             Task title (required)\n", "  --workspace ID           Workspace ID (required unless the credential file\n                           names it)\n", "  --session ID             Session ID (required unless the credential file names\n                           it)\n", "  --description TEXT       Task description\n", "  --credential-file FILE   "} {
		if !strings.Contains(create, want) {
			t.Fatalf("task create help misses %q:\n%s", want, create)
		}
	}
	if list := helpOutput(t, "help", "session", "list"); !strings.Contains(list, "Workspace ID (required unless") || strings.Contains(list, "Session ID (required") {
		t.Fatalf("session list help:\n%s", list)
	}
	send := helpOutput(t, "help", "mail", "send")
	if !strings.Contains(send, "  --task ID                Task ID (required)\n") || !strings.Contains(send, "  --mention NAME  ") || strings.Contains(send, "--body TEXT              Mail text (required)") {
		t.Fatalf("mail send help:\n%s", send)
	}
	serve := helpOutput(t, "serve", "--help")
	if !strings.Contains(serve, "  --listen ADDR  ") || !strings.Contains(serve, "  --herdr-executable FILE  ") || strings.Contains(serve, "--credential-file") || strings.Contains(serve, "--as ") {
		t.Fatalf("serve help:\n%s", serve)
	}
	group := helpOutput(t, "help", "task")
	if group != helpOutput(t, "task", "--help") || !strings.Contains(group, "\n  task create ") || !strings.Contains(group, "\n  task attention resolve ") || strings.Contains(group, "dependency add") {
		t.Fatalf("task group help:\n%s", group)
	}
	checkWidth(t, "help task", group)
}

func TestHelpJSONStaysCompatible(t *testing.T) {
	var help struct {
		Data struct {
			Commands []string `json:"commands"`
			Flags    []string `json:"flags"`
		} `json:"data"`
	}
	out := helpOutput(t, "--help", "--json")
	if json.Unmarshal([]byte(out), &help) != nil || helpOutput(t, "task", "create", "--help", "--json") != out || helpOutput(t, "-h", "--json") != out {
		t.Fatalf("help json %s", out)
	}
	old := []string{"serve", "open", "version", "health", "storage status", "storage backup", "notifications status", "notifications drain"}
	old = append(append(append(old, lifecycleCommands...), taskCommands...), mailCommands...)
	if len(help.Data.Commands) != len(old)+16 || strings.Join(help.Data.Commands[:len(old)], ",") != strings.Join(old, ",") || strings.Join(help.Data.Commands[len(old):], ",") != "new,skill install,skill uninstall,update,view revoke,service install,service uninstall,service status,status,watch,hook install,hook uninstall,hook,statusline,herdr bind,herdr focus" {
		t.Fatalf("commands changed: %v", help.Data.Commands)
	}
	added := map[string]bool{"-h": true, "-r": true, "--resume": true, "--version": true, "--project": true, "--refresh": true, "--check": true, "--all": true, "--notify": true, "--notify-text": true, "--claude": true, "--codex": true, "--pi": true, "--opencode": true, "--orchestrator": true, "--no-stop-guard": true, "--no-mail-nudge": true}
	before := []string{}
	for _, f := range help.Data.Flags {
		if !added[f] {
			before = append(before, f)
		}
	}
	if strings.Join(before, " ") != "--after --after-task --agent --as --body --body-file --clear-runtime --credential-file --cursor --cursor-file --description --destination --expected-revision --from --help --herdr-executable --json --known-revision --limit --listen --location --mention --name --new-credential-file --no-browser --note --operation-id --page-kind --path --progress --question --read-revision --reason --reference --reference-revision --report --runtime-file --runtime-id --runtime-state --session --state-dir --summary --task --task-limit --ticket --title --to --workspace --worktree" || len(help.Data.Flags) != len(before)+len(added) {
		t.Fatalf("flags changed: %v", help.Data.Flags)
	}
}
