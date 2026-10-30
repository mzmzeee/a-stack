package stack

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/rogpeppe/go-internal/testscript"
)

// This story lives with the CLI stories, but owns the notifier's fake clock.
// It runs the real CLI hooks against the same disposable service.
func TestQuietHourScenario(t *testing.T) {
	f := noticeTestFixture(t)

	binary := filepath.Join(t.TempDir(), "a-stack")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/a-stack")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}

	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 3, AgentID: f.orchID})

	binding := RuntimeBinding{Endpoint: f.config.Binding.Endpoint, Harness: "codex", HarnessVersion: "0.164.0"}
	binding.InstanceID, binding.HerdrAlias, binding.PaneID, binding.TerminalID, binding.NativeSessionID = "quiet-orch", "quiet-orch", "w1:p3", "quiet-terminal", "quiet-native"
	if _, err := f.service.Store.BindAgent(ctx, f.orch.Token, "attach", AgentBindingRequest{Context: f.ref(0), OperationID: opaqueID(), AgentID: f.orchID, ExpectedRevision: 1, Runtime: &binding}); err != nil {
		t.Fatal(err)
	}

	f.config.Targets = append(f.config.Targets, binding)
	f.write(t)

	senders := []Credential{f.a}
	revision := int64(4)
	for i := 0; i < 4; i++ {
		credential, agent := f.identityFixture.register(t, 0, fmt.Sprintf("quiet-worker-%d", i))
		f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: revision, AgentID: agent.ResourceID})
		revision++
		senders = append(senders, credential)
	}

	clock := time.Now()
	f.n.clock = func() time.Time { return clock }
	minute := 0
	testscript.Run(t, testscript.Params{
		Dir: "../../cmd/a-stack/testdata/scenarios/quiet",
		Setup: func(e *testscript.Env) error {
			e.Vars = []string{"PATH=" + os.Getenv("PATH"), "WORK=" + e.WorkDir}
			for _, key := range []string{"HOME", "XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "CODEX_HOME"} {
				dir := filepath.Join(e.WorkDir, key)
				if err := os.MkdirAll(dir, 0o700); err != nil {
					return err
				}

				e.Setenv(key, dir)
			}

			e.Setenv("BIN", binary)
			e.Setenv("STATE", f.identityFixture.dir)
			e.Setenv("WS", f.workspace)
			e.Setenv("SESSION", f.sessions[0])
			e.Setenv("AGENT", f.orchID)

			project := filepath.Join(e.WorkDir, "project")
			if err := os.Mkdir(project, 0o700); err != nil {
				return err
			}

			e.Setenv("PROJECT", project)
			e.Cd = project
			credential := f.orch
			credential.WorkspaceID = f.workspace
			credential.SessionID = f.sessions[0]
			credential.AgentName = "01-orchestrator"
			data, _ := json.Marshal(credential)
			file := filepath.Join(e.WorkDir, "orch.json")
			e.Setenv("CRED", file)

			return os.WriteFile(file, data, 0o600)
		},
		Cmds: map[string]func(*testscript.TestScript, bool, []string){
			"minutes": func(ts *testscript.TestScript, neg bool, args []string) {
				var count int

				if neg || len(args) != 1 {
					ts.Fatalf("minutes COUNT")
				}

				if _, err := fmt.Sscan(args[0], &count); err != nil || count < 1 || count > 60 {
					ts.Fatalf("bad minute count")
				}

				for range count {
					for i, sender := range senders {
						body := fmt.Sprintf("Progress minute %d worker %d", minute, i)
						if i%2 == 0 {
							body += " @01-orchestrator @03-worker"
						}

						f.send(t, sender.Token, body)
					}

					f.drain(t, 4)
					f.read(t, f.b.Token, "", 100)
					clock = clock.Add(time.Minute)
					minute++
				}
			},
			"pane-count": func(ts *testscript.TestScript, neg bool, args []string) {
				if neg || len(args) != 2 {
					ts.Fatalf("pane-count orch|worker COUNT")
				}

				pane := f.config.Binding.PaneID
				if args[0] == "orch" {
					pane = binding.PaneID
				}

				got := 0
				for _, call := range f.calls(t) {
					if len(call.Args) > 2 && call.Args[1] == "prompt" && call.Args[2] == pane {
						got++
					}
				}

				if fmt.Sprint(got) != args[1] {
					ts.Fatalf("%s typed notices = %d, want %s", args[0], got, args[1])
				}
			},
			"binding-mode": func(ts *testscript.TestScript, neg bool, args []string) {
				if neg || len(args) != 1 {
					ts.Fatalf("binding-mode MODE")
				}

				var encoded string
				if err := f.service.Store.db.QueryRow("SELECT binding FROM agents WHERE id=?", f.orchID).Scan(&encoded); err != nil {
					ts.Fatalf("binding read: %v", err)
				}

				var stored map[string]any
				if json.Unmarshal([]byte(encoded), &stored) != nil || stored["notices"] != args[0] {
					ts.Fatalf("incorrect binding mode")
				}
			},
			"hook-event": func(ts *testscript.TestScript, neg bool, args []string) {
				if neg || len(args) != 1 {
					ts.Fatalf("hook-event EVENT")
				}

				input, _ := json.Marshal(map[string]string{"hook_event_name": args[0], "cwd": ts.Getenv("PROJECT"), "session_id": "quiet-native"})
				file := filepath.Join(ts.Getenv("WORK"), "event.json")
				if err := os.WriteFile(file, input, 0o600); err != nil {
					ts.Fatalf("event write: %v", err)
				}
			},
		},
	})
}
