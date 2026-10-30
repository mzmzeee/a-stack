package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The released parent discards the child's unknown warnings field. The next
// explicit Agent command must still explain why the migrated hooks are silent.
func TestOldUpdaterRetainsEnrollmentDiagnostic(t *testing.T) {
	for _, harness := range []string{"claude", "codex", "pi", "opencode"} {
		t.Run(harness, func(t *testing.T) {
			f := newTeam(t)
			hookHome(t)
			withShims(t, true)
			t.Chdir(f.project)

			_, errout, code := f.run("", "hook", "install", "--"+harness, "--credential-file", f.workerFile)
			if code != 0 {
				t.Fatal(errout)
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

			if err = os.WriteFile(file, bytes.ReplaceAll(data, []byte(" --harness "+harness), nil), 0o600); err != nil {
				t.Fatal(err)
			}

			var child bytes.Buffer
			if err = refreshInstalledFiles(true, &child); err != nil {
				t.Fatal(err)
			}

			// Exactly the v0.1.8 parent shape: no Warnings member.
			var oldParent struct {
				Data struct {
					Skills, Files  []string
					RefreshedFiles []string `json:"refreshed_files"`
				}
			}
			if err = json.Unmarshal(child.Bytes(), &oldParent); err != nil {
				t.Fatal(err)
			}

			parentOut, _ := json.Marshal(oldParent)
			if bytes.Contains(parentOut, []byte("enroll")) {
				t.Fatal("old-parent fixture accidentally preserved the warning")
			}

			child.Reset()

			if err = refreshInstalledFiles(true, &child); err != nil {
				t.Fatal(err)
			}

			// No cwd adoption: the same credential gets its notice outside the worktree.
			t.Chdir(t.TempDir())

			for _, args := range [][]string{{"session", "inspect"}, {"mail", "check"}, {"task", "show", "--task", f.task}} {
				out, errout, code := f.run(f.workerFile, args...)
				if code != 0 || !json.Valid([]byte(out)) || strings.Count(errout, "hooks are installed but not enrolled") != 1 || !strings.Contains(errout, "me hook enroll --"+harness) {
					t.Fatalf("lost pending diagnostic: %d %s", code, errout)
				}
			}

			_, otherErr, code := f.run(f.orchFile, "session", "inspect")
			if code != 0 || strings.Contains(otherErr, "hook enroll") {
				t.Fatal("other credential inherited migration notice")
			}

			t.Chdir(f.project)

			for _, machine := range []bool{false, true} {
				args := []string{"hook", "status", "--" + harness}
				if machine {
					args = append(args, "--json")
				}

				out, errout, code := f.run(f.workerFile, args...)
				if code != 0 {
					t.Fatal(errout)
				}

				if machine && (!strings.Contains(out, `"migration_pending":true`) || !strings.Contains(out, `"session_known":false`)) {
					t.Fatal(out)
				}

				if !machine && !strings.Contains(out, "migration pending=true") {
					t.Fatal(out)
				}
			}

			_, errout, code = f.run(f.workerFile, "hook", "enroll", "--"+harness, "--native-session", "native")
			if code != 0 {
				t.Fatal(errout)
			}

			_, errout, code = f.run(f.workerFile, "session", "inspect")
			if code != 0 || errout != "" {
				t.Fatal("enrollment did not clear notice", errout)
			}

			out, errout, code := f.run(f.workerFile, "hook", "status", "--"+harness, "--native-session", "native", "--json")
			if code != 0 || !strings.Contains(out, `"enrolled":true`) || !strings.Contains(out, `"urgent_mail":true`) || !strings.Contains(out, `"migration_pending":false`) {
				t.Fatal(out, errout)
			}

			out, errout, code = f.run(f.workerFile, "hook", "status", "--"+harness, "--native-session", "unrelated", "--json")
			if code != 0 || strings.Contains(out, `"urgent_mail":true`) || strings.Contains(out, `"enrolled":true`) || !strings.Contains(out, `"agent_enrolled":true`) {
				t.Fatal("status adopted another session", out, errout)
			}

			f.receipt(f.workerFile, f.scoped("agent", "attach", "--agent", f.worker, "--runtime-id", "new-binding", "--expected-revision", "1", "--operation-id", "status-binding-change")...)

			out, errout, code = f.run(f.workerFile, "hook", "status", "--"+harness, "--native-session", "native", "--json")
			if code != 0 || strings.Contains(out, `"urgent_mail":true`) || strings.Contains(out, `"agent_enrolled":true`) || strings.Contains(out, `"enrolled":true`) {
				t.Fatal("status accepted stale binding", out, errout)
			}
		})
	}
}

func TestPendingHooksAreWorktreeScoped(t *testing.T) {
	f := newTeam(t)
	hookHome(t)
	t.Chdir(f.project)

	id := hookIdentity{credential: f.workerFile, agent: f.worker, worktree: f.project, harness: "claude"}
	if err := markHookPending(id); err != nil {
		t.Fatal(err)
	}

	defaultHarness := id
	defaultHarness.harness = "codex"
	if err := markHookPending(defaultHarness); err != nil {
		t.Fatal(err)
	}

	other := id
	other.worktree = t.TempDir()
	if err := markHookPending(other); err != nil {
		t.Fatal(err)
	}

	if err := enrollHook(id, "claude", "native", "", 1); err != nil {
		t.Fatal(err)
	}

	pending := pendingHooks(f.workerFile)

	canonical, err := filepath.EvalSymlinks(other.worktree)
	if err != nil {
		t.Fatal(err)
	}

	if len(pending) != 1 || pending[0].Worktree != canonical {
		t.Fatal("enrollment cleared another worktree")
	}

	before := hookFiles(t)
	if runEnrollmentEvent(t, id, "unrelated", "", f.project, "SessionStart", "") != "" || len(before) != len(hookFiles(t)) {
		t.Fatal("pending diagnostic escaped into hook events")
	}
}
