package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func hookFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}

	root := testUserCacheDir(t)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}

		if err != nil {
			return err
		}

		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			info, err := entry.Info()
			if err != nil {
				return err
			}

			files[path] = string(data) + info.ModTime().String()
		} else {
			files[path] = "directory"
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return files
}

func runEnrollmentEvent(t *testing.T, id hookIdentity, session, subagent, cwd, event, source string) string {
	t.Helper()

	data, err := json.Marshal(hookInput{SessionID: session, AgentID: subagent, Cwd: cwd, Event: event, Source: source})
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	runHook(id, bytes.NewReader(data), &out)
	return out.String()
}

func TestHookEnrollmentIdentityMatrix(t *testing.T) {
	f := newTeam(t)
	home := hookHome(t)
	f.receipt(f.workerFile, f.scoped("agent", "attach", "--agent", f.worker, "--runtime-id", "matrix-runtime", "--expected-revision", "1", "--operation-id", "matrix-attach")...)
	f.taskChange(f.workerFile, "matrix-working", 2, "status", "--progress", "working")
	child := filepath.Join(f.project, "child")

	sibling := filepath.Join(filepath.Dir(f.project), "sibling")
	prefix := f.project + "-other"
	for _, dir := range []string{child, sibling, prefix} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	alias := filepath.Join(filepath.Dir(f.project), "alias")
	aliasOK := os.Symlink(f.project, alias) == nil
	id := hookIdentity{harness: "claude", state: f.state, credential: f.workerFile, agent: f.worker, worktree: f.project, ref: stack.Context{WorkspaceID: f.workspace, SessionID: f.session}}
	id.runtimeID = "matrix-runtime"
	enrollTestHook(t, id, "enrolled", "")

	paths := []struct {
		name, path string
		allowed    bool
	}{
		{"exact", f.project, true}, {"child", child, true}, {"sibling", sibling, false}, {"prefix", prefix, false}, {"home", home, false}, {"ancestor", filepath.Dir(f.project), false},
	}
	if aliasOK {
		paths = append(paths, struct {
			name, path string
			allowed    bool
		}{"alias", alias, true})
	}

	for _, p := range paths {
		for _, session := range []string{"enrolled", "unrelated", ""} {
			for _, event := range []string{"SessionStart", "PostToolUse", "UserPromptSubmit", "PermissionRequest", "Stop"} {
				t.Run(p.name+"/"+session+"/"+event, func(t *testing.T) {
					before := hookFiles(t)

					state := runtimeState(t, f)
					out := runEnrollmentEvent(t, id, session, "", p.path, event, "compact")
					if p.allowed && session == "enrolled" {
						if event == "SessionStart" && out == "" {
							t.Fatal("enrolled session lost its reminder")
						}
					} else {
						if out != "" {
							t.Fatal("unenrolled event received output")
						}

						if !reflect.DeepEqual(before, hookFiles(t)) {
							t.Fatal("unenrolled event changed hook cache")
						}

						if state != runtimeState(t, f) {
							t.Fatal("unenrolled event changed runtime state")
						}
					}
				})
			}
		}
	}

	// Main enrollment never enrolls its subagents, including in the same folder.
	for _, cwd := range []string{f.project, child} {
		before := hookFiles(t)
		if runEnrollmentEvent(t, id, "enrolled", "sub", cwd, "SubagentStart", "") != "" {
			t.Fatal("parent identity reached subagent")
		}

		if !reflect.DeepEqual(before, hookFiles(t)) {
			t.Fatal("subagent changed parent cache")
		}
	}

	// Explicit subagent enrollment is exact in both parent and subagent IDs.
	enrollTestHook(t, id, "parent", "sub")

	for _, tc := range []struct {
		session, sub, cwd string
		want              bool
	}{
		{"parent", "sub", f.project, true}, {"parent", "sub", child, false}, {"parent", "", f.project, false}, {"parent", "other", f.project, false}, {"other", "sub", f.project, false},
	} {
		before := hookFiles(t)
		out := runEnrollmentEvent(t, id, tc.session, tc.sub, tc.cwd, "SubagentStart", "")
		if (out != "") != tc.want {
			t.Fatal("wrong subagent enrollment decision")
		}

		if !tc.want && !reflect.DeepEqual(before, hookFiles(t)) {
			t.Fatal("unrelated subagent changed cache")
		}
	}
}

func TestHookLegacyBroadEnrollmentStaysSilent(t *testing.T) {
	f := newTeam(t)
	home := hookHome(t)
	id := hookIdentity{harness: "claude", state: f.state, credential: f.workerFile, agent: f.worker, worktree: f.project, ref: stack.Context{WorkspaceID: f.workspace, SessionID: f.session}}
	enrollTestHook(t, id, "native", "")

	file, err := enrollmentFile(f.worker)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	var e hookEnrollment
	if err = json.Unmarshal(data, &e); err != nil {
		t.Fatal(err)
	}

	for _, root := range []string{home, filepath.Dir(home)} {
		id.worktree = root
		e.Worktree = root

		data, _ = json.Marshal(e)
		if err = os.WriteFile(file, data, 0o600); err != nil {
			t.Fatal(err)
		}

		before := hookFiles(t)
		for _, event := range []string{"SessionStart", "Stop", "PostToolUse"} {
			if runEnrollmentEvent(t, id, "native", "", home, event, "") != "" {
				t.Fatal("broad enrollment acted")
			}
		}

		if !reflect.DeepEqual(before, hookFiles(t)) {
			t.Fatal("broad enrollment changed cache")
		}
	}
}

func TestHookEnrollmentRejectsStaleIdentity(t *testing.T) {
	f := newTeam(t)
	hookHome(t)
	id := hookIdentity{harness: "claude", state: f.state, credential: f.workerFile, agent: f.worker, worktree: f.project, ref: stack.Context{WorkspaceID: f.workspace, SessionID: f.session}}
	enrollTestHook(t, id, "same", "")

	file, err := enrollmentFile(f.worker)
	if err != nil {
		t.Fatal(err)
	}

	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{"machine", "credential", "generation", "harness", "session", "worktree"} {
		var e hookEnrollment
		if err = json.Unmarshal(original, &e); err != nil {
			t.Fatal(err)
		}

		switch field {
		case "machine":
			e.Machine = "another-machine"
		case "credential":
			e.Credential = "old-credential"
		case "generation":
			e.Revision++
		case "harness":
			e.Harness = "codex"
		case "session":
			e.Session = "old-native-session"
		case "worktree":
			e.Worktree = filepath.Dir(f.project)
		}

		data, _ := json.Marshal(e)
		if err = os.WriteFile(file, data, 0o600); err != nil {
			t.Fatal(err)
		}

		before := hookFiles(t)
		for _, event := range []string{"SessionStart", "Stop", "PostToolUse", "PermissionRequest"} {
			if runEnrollmentEvent(t, id, "same", "", f.project, event, "") != "" {
				t.Fatalf("%s mismatch acted", field)
			}
		}

		if !reflect.DeepEqual(before, hookFiles(t)) {
			t.Fatalf("%s mismatch wrote cache", field)
		}
	}

	if err = os.WriteFile(file, original, 0o600); err != nil {
		t.Fatal(err)
	}

	// Agent identity revision changes on attach/rebind, not ordinary observation.
	f.receipt(f.workerFile, f.scoped("agent", "attach", "--agent", f.worker, "--runtime-id", "replacement", "--expected-revision", "1", "--operation-id", "enroll-replace")...)

	before := hookFiles(t)
	if runEnrollmentEvent(t, id, "same", "", f.project, "SessionStart", "") != "" {
		t.Fatal("old generation survived binding change")
	}

	if !reflect.DeepEqual(before, hookFiles(t)) {
		t.Fatal("stale generation wrote cache")
	}

	enrollTestHook(t, id, "new", "")

	if runEnrollmentEvent(t, id, "same", "", f.project, "SessionStart", "") != "" {
		t.Fatal("new enrollment retained old native session")
	}

	if runEnrollmentEvent(t, id, "new", "", f.project, "SessionStart", "compact") == "" {
		t.Fatal("new enrollment failed")
	}
}

func TestHerdrBindEnrollsVerifiedNativeSession(t *testing.T) {
	if !posixModes {
		t.Skip("fake executable uses /bin/sh")
	}

	for _, harness := range []string{"claude", "codex", "pi", "opencode"} {
		t.Run(harness, func(t *testing.T) {
			f := newTeam(t)
			hookHome(t)
			t.Chdir(f.project)

			bin := t.TempDir()
			session, kind := "bound-native", "id"
			if harness == "pi" {
				session, kind = filepath.Join(bin, "renamed.jsonl"), "path"
				if err := os.WriteFile(session, []byte(`{"type":"session","id":"bound-native"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			metadata, err := json.Marshal(map[string]any{"result": map[string]any{"agent": map[string]any{
				"name": "worker", "pane_id": "w1:p1", "terminal_id": "terminal-one", "agent": harness,
				"agent_session": map[string]string{"agent": harness, "kind": kind, "source": "herdr:" + harness, "value": session},
			}}})
			if err != nil {
				t.Fatal(err)
			}

			fake := "#!/bin/sh\ncase \"$1 $2\" in\n'status server') printf '%s\\n' '{\"running\":true,\"protocol\":22,\"compatible\":true,\"endpoint_compatible\":true}';;\n'agent get') printf '%s\\n' " + shellQuote(string(metadata)) + ";;\n*) exit 1;;\nesac\n"
			if err = os.WriteFile(filepath.Join(bin, "herdr"), []byte(fake), 0o700); err != nil {
				t.Fatal(err)
			}

			if err = os.WriteFile(filepath.Join(bin, harness), []byte("#!/bin/sh\nprintf '1.4.0\\n'\n"), 0o700); err != nil {
				t.Fatal(err)
			}

			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("HERDR_ENV", "1")
			t.Setenv("HERDR_PANE_ID", "w1:p1")
			t.Setenv("HERDR_SOCKET_PATH", filepath.Join(bin, "socket"))
			f.receipt(f.workerFile, f.scoped("herdr", "bind", "--agent", f.worker, "--expected-revision", "1", "--operation-id", "bind-enroll")...)

			id := hookIdentity{harness: harness, state: f.state, credential: f.workerFile, agent: f.worker, worktree: f.project, ref: stack.Context{WorkspaceID: f.workspace, SessionID: f.session}}
			if runEnrollmentEvent(t, id, "bound-native", "", f.project, "SessionStart", "") == "" {
				t.Fatal("bound session not enrolled")
			}

			before := hookFiles(t)
			if runEnrollmentEvent(t, id, "unrelated", "", f.project, "SessionStart", "") != "" {
				t.Fatal("bind enrolled unrelated session")
			}

			if !reflect.DeepEqual(before, hookFiles(t)) {
				t.Fatal("unrelated session wrote cache")
			}
		})
	}
}

func TestHookNativeSessionUsesPiHeaderNotFilename(t *testing.T) {
	file := filepath.Join(t.TempDir(), "renamed.jsonl")
	for _, data := range []string{`{"type":"session","id":"custom-native-id"}` + "\n" + `{"type":"message","private":"not needed"}`, `{"type":"message","id":"wrong"}`, "not json", `{"type":"session"}`} {
		if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}

		id, err := hookNativeSession(&stack.RuntimeBinding{Harness: "pi", NativeSessionID: file})
		if strings.Contains(data, "custom-native-id") {
			if err != nil || id != "custom-native-id" {
				t.Fatal("Pi header identity lost")
			}
		} else if err == nil {
			t.Fatal("invalid Pi header accepted")
		}
	}

	for _, harness := range []string{"claude", "codex", "opencode"} {
		id, err := hookNativeSession(&stack.RuntimeBinding{Harness: harness, NativeSessionID: "native"})
		if err != nil || id != "native" {
			t.Fatal("native ID changed")
		}
	}
}

func TestHookInstallExplicitlyEnrollsOnlyOneHarness(t *testing.T) {
	f := newTeam(t)
	hookHome(t)
	t.Chdir(f.project)

	for _, harness := range []string{"claude", "codex", "pi", "opencode"} {
		if !shimsSupported && (harness == "pi" || harness == "opencode") {
			continue
		}

		_, errout, code := f.run("", "hook", "install", "--"+harness, "--credential-file", f.workerFile, "--native-session", "native")
		if code != 0 {
			t.Fatalf("%s enrollment: %d %s", harness, code, errout)
		}

		id := hookIdentity{harness: harness, state: f.state, credential: f.workerFile, agent: f.worker, worktree: f.project, ref: stack.Context{WorkspaceID: f.workspace, SessionID: f.session}}
		if runEnrollmentEvent(t, id, "native", "", f.project, "SessionStart", "") == "" {
			t.Fatalf("%s not enrolled", harness)
		}
	}

	_, _, code := f.run("", "hook", "install", "--claude", "--codex", "--credential-file", f.workerFile, "--native-session", "other")
	if code != 2 {
		t.Fatal("ambiguous enrollment accepted")
	}
}
