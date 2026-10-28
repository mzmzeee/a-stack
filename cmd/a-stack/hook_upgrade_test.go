package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestRefreshMigratesReleasedHookCommands(t *testing.T) {
	for _, harness := range []string{"claude", "codex", "pi", "opencode"} {
		for _, machine := range []bool{false, true} {
			t.Run(harness+"/"+map[bool]string{false: "human", true: "json"}[machine], func(t *testing.T) {
				f := newTeam(t)
				hookHome(t)
				withShims(t, true)
				t.Chdir(f.project)
				out, errout, code := f.run("", "hook", "install", "--"+harness, "--credential-file", f.workerFile)
				if code != 0 || !strings.Contains(out, "Hooks stay silent until enrollment") {
					t.Fatalf("install: %d %s %s", code, out, errout)
				}
				flags := hookFlags{claude: harness == "claude", codex: harness == "codex", pi: harness == "pi", opencode: harness == "opencode"}
				configs, err := hookSettings(flags, false)
				if err != nil {
					t.Fatal(err)
				}
				file := configs[harness]
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				legacy := bytes.ReplaceAll(data, []byte(" --harness "+harness), nil)
				var doc map[string]any
				if err = json.Unmarshal(legacy, &doc); err != nil {
					t.Fatal(err)
				}
				doc["foreign-setting"] = "preserve"
				groups := doc["hooks"].(map[string]any)["Stop"].([]any)
				hooks := groups[0].(map[string]any)["hooks"].([]any)
				old := hooks[0].(map[string]any)["command"].(string)
				if !legacyHookCommand(old) {
					t.Fatal("released grammar not recognized")
				}
				foreign := old + " && echo custom"
				groups[0].(map[string]any)["hooks"] = append(hooks, map[string]any{"type": "command", "command": foreign, "custom": true})
				legacy, _ = json.Marshal(doc)
				if err = os.WriteFile(file, legacy, 0o640); err != nil {
					t.Fatal(err)
				}
				var refresh bytes.Buffer
				if err = refreshInstalledFiles(machine, &refresh); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(refresh.String(), "Each Agent must enroll") {
					t.Fatal("migration message missing")
				}
				after, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(after, &doc); err != nil {
					t.Fatal(err)
				}
				if doc["foreign-setting"] != "preserve" {
					t.Fatal("foreign settings changed")
				}
				commands := commands(readSettings(t, file), "Stop")
				if len(commands) != 2 || commands[0] != old+" --harness "+harness || commands[1] != foreign {
					t.Fatal("migration changed wrong command")
				}
				id := hookIdentity{harness: harness, state: f.state, credential: f.workerFile, agent: f.worker, worktree: f.project, ref: stack.Context{WorkspaceID: f.workspace, SessionID: f.session}}
				before := hookFiles(t)
				if runEnrollmentEvent(t, id, "native", "", f.project, "SessionStart", "") != "" {
					t.Fatal("migration enrolled by folder")
				}
				if len(before) != len(hookFiles(t)) {
					t.Fatal("migration event wrote cache")
				}
				_, errout, code = f.run(f.workerFile, "hook", "enroll", "--"+harness, "--native-session", "native")
				if code != 0 {
					t.Fatalf("one-step enrollment: %d %s", code, errout)
				}
				if runEnrollmentEvent(t, id, "native", "", f.project, "SessionStart", "") == "" {
					t.Fatal("migrated enrolled hook silent")
				}
				refresh.Reset()
				if err = refreshInstalledFiles(false, &refresh); err != nil || refresh.Len() != 0 {
					t.Fatalf("migration repeated: %v %s", err, refresh.String())
				}
			})
		}
	}
}

func TestHookEnrollDetectsOnlyExplicitCurrentSession(t *testing.T) {
	for harness, variable := range map[string]string{"claude": "CLAUDE_CODE_SESSION_ID", "codex": "CODEX_THREAD_ID", "pi": "PI_SESSION_ID", "opencode": "OPENCODE_SESSION_ID"} {
		t.Run(harness, func(t *testing.T) {
			f := newTeam(t)
			hookHome(t)
			t.Chdir(f.project)
			before := hookFiles(t)
			_, _, code := f.run(f.workerFile, "hook", "enroll", "--"+harness)
			if code != 2 || len(before) != len(hookFiles(t)) {
				t.Fatal("missing metadata enrolled or wrote cache")
			}
			t.Setenv(variable, "current-native")
			out, errout, code := f.run(f.workerFile, "hook", "enroll", "--json")
			if code != 0 || !strings.Contains(out, `"enrolled":true`) {
				t.Fatalf("detected enrollment: %d %s", code, errout)
			}
			id := hookIdentity{harness: harness, state: f.state, credential: f.workerFile, agent: f.worker, worktree: f.project, ref: stack.Context{WorkspaceID: f.workspace, SessionID: f.session}}
			if runEnrollmentEvent(t, id, "current-native", "", f.project, "SessionStart", "") == "" {
				t.Fatal("detected session not enrolled")
			}
			if runEnrollmentEvent(t, id, "unrelated", "", f.project, "SessionStart", "") != "" {
				t.Fatal("unrelated session enrolled")
			}
		})
	}
}

func TestHookEnrollKeepsInstalledRuntimeObservation(t *testing.T) {
	f := newTeam(t)
	hookHome(t)
	t.Chdir(f.project)
	f.receipt(f.workerFile, f.scoped("agent", "attach", "--agent", f.worker, "--runtime-id", "installed-runtime", "--expected-revision", "1", "--operation-id", "enroll-existing-runtime")...)
	t.Setenv("CODEX_THREAD_ID", "native")
	_, errout, code := f.run(f.workerFile, "hook", "enroll", "--codex")
	if code != 0 {
		t.Fatal(errout)
	}
	id := hookIdentity{harness: "codex", state: f.state, credential: f.workerFile, agent: f.worker, worktree: f.project, runtimeID: "installed-runtime", ref: stack.Context{WorkspaceID: f.workspace, SessionID: f.session}}
	runEnrollmentEvent(t, id, "native", "", f.project, "UserPromptSubmit", "")
	if runtimeState(t, f) != "working" {
		t.Fatal("enroll required the user to repeat an already installed runtime ID")
	}
}

func TestHookEnrollAmbiguousAndPiMetadata(t *testing.T) {
	hookHome(t)
	t.Setenv("PI_SESSION_ID", "pi")
	t.Setenv("CODEX_THREAD_ID", "codex")
	flags := hookFlags{}
	if flags.detectNativeSession() == nil {
		t.Fatal("ambiguous inherited sessions accepted")
	}
	flags = hookFlags{codex: true}
	if err := flags.detectNativeSession(); err != nil || flags.nativeSession != "codex" {
		t.Fatal("explicit harness lost", err)
	}
	t.Setenv("PI_SESSION_ID", "")
	file := filepath.Join(t.TempDir(), "renamed.jsonl")
	if err := os.WriteFile(file, []byte(`{"type":"session","id":"header-native"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_SESSION_FILE", file)
	flags = hookFlags{pi: true}
	if err := flags.detectNativeSession(); err != nil || flags.nativeSession != "header-native" {
		t.Fatal("Pi header fallback lost", err)
	}
}

func TestRefreshMigratesProjectHooksAndPreservesLinks(t *testing.T) {
	f := newTeam(t)
	hookHome(t)
	t.Chdir(f.project)
	_, errout, code := f.run("", "hook", "install", "--claude", "--project", "--credential-file", f.workerFile)
	if code != 0 {
		t.Fatal(errout)
	}
	configs, err := hookSettings(hookFlags{claude: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	file := configs["claude"]
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	legacy := bytes.ReplaceAll(data, []byte(" --harness claude"), nil)
	if err = os.WriteFile(file, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = refreshInstalledFiles(false, &out); err != nil || !strings.Contains(out.String(), hookMigrationNotice) {
		t.Fatal("project migration missing", err)
	}
	target := filepath.Join(t.TempDir(), "linked-config")
	if err = os.WriteFile(target, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, file); err != nil {
		t.Skip("symlink unavailable")
	}
	if changed, err := refreshHookCommands(file, "claude"); err != nil || changed {
		t.Fatal("linked config rewritten", err)
	}
	after, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(after, legacy) {
		t.Fatal("linked target changed")
	}
}

func TestUpdateCarriesRefreshEnrollmentWarning(t *testing.T) {
	unixOnly(t, "fake updater uses /bin/sh")
	for _, machine := range []bool{false, true} {
		t.Run(map[bool]string{false: "human", true: "json"}[machine], func(t *testing.T) {
			warning, _ := json.Marshal([]string{hookMigrationNotice})
			binary := strings.Replace(fakeBinary("v0.2.0"), `"refreshed_files":[]`, `"refreshed_files":[],"warnings":`+string(warning), 1)
			release := &fakeRelease{latest: "v0.2.0", archives: map[string][]byte{"v0.2.0": tarGz(t, map[string]string{"a-stack": binary})}}
			updateSetup(t, "v0.1.0", release)
			t.Setenv("A_STACK_TEST_REFRESH", "")
			args := []string{}
			if machine {
				args = append(args, "--json")
			}
			out, _ := runUpdate(t, 0, args...)
			if strings.Count(out, hookMigrationNotice) != 1 {
				t.Fatal("updater lost or repeated migration warning")
			}
		})
	}
}

func TestLegacyHookRecognitionRejectsForeignCommands(t *testing.T) {
	f := newTeam(t)
	hookHome(t)
	id := strings.Repeat("a", 43)
	exe, _ := os.Executable()
	command := quotePath(exe) + " hook --credential-file " + quotePath(f.workerFile) + " --workspace " + id + " --session " + id + " --agent " + id + " --runtime-id " + shellQuote("it's-me") + " --worktree " + quotePath(f.project)
	if !legacyHookCommand(command) {
		t.Fatal("quoted released command rejected")
	}
	for _, foreign := range []string{command + " --extra", command + "; echo hi", "echo " + command, strings.Replace(command, " hook ", " custom ", 1), strings.Replace(command, quotePath(exe), quotePath(filepath.Join(f.project, "foreign")), 1)} {
		if legacyHookCommand(foreign) {
			t.Fatal("foreign command recognized")
		}
	}
}
