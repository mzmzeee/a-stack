package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
	"github.com/rogpeppe/go-internal/testscript"
)

func TestRegressionScenarios(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "a-stack")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	testscript.Run(t, testscript.Params{
		Dir: "testdata/scenarios",
		Setup: func(e *testscript.Env) error {
			// No inherited credential, proxy, harness or service selectors.
			e.Vars = []string{"PATH=" + os.Getenv("PATH"), "WORK=" + e.WorkDir, "SYSTEMROOT=" + os.Getenv("SYSTEMROOT")}
			e.Setenv("BIN", binary)
			for _, key := range []string{"HOME", "USERPROFILE", "XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "LOCALAPPDATA", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "PI_CODING_AGENT_DIR", "OPENCODE_CONFIG_DIR", "TMPDIR", "TMP", "TEMP"} {
				dir := filepath.Join(e.WorkDir, key)
				if key == "USERPROFILE" {
					dir = filepath.Join(e.WorkDir, "HOME")
				}
				if err := os.MkdirAll(dir, 0700); err != nil {
					return err
				}
				e.Setenv(key, dir)
			}
			state := filepath.Join(e.WorkDir, "state")
			e.Setenv("STATE", state)
			project := filepath.Join(e.Getenv("HOME"), "project")
			if err := os.MkdirAll(project, 0700); err != nil {
				return err
			}
			e.Setenv("PROJECT", project)
			e.Cd = project
			service, err := stack.Start(context.Background(), state, "")
			if err != nil {
				return err
			}
			done := make(chan error, 1)
			go func() { done <- service.Serve() }()
			e.Defer(func() { _ = service.Close(context.Background()); <-done })
			invoke := func(actor string, args ...string) (string, error) {
				args = append(args, "--state-dir", state, "--json")
				if actor != "" {
					args = append(args, "--as", "agent", "--credential-file", e.Getenv(actor))
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, binary, args...)
				cmd.Env, cmd.Dir = e.Vars, project
				output, err := cmd.Output()
				if err != nil {
					return "", fmt.Errorf("fixture %s: %w (%s)", args[0], err, cmdErrorMessage(err))
				}
				var envelope struct {
					Data stack.Receipt `json:"data"`
				}
				if err := json.Unmarshal(output, &envelope); err != nil {
					return "", err
				}
				return envelope.Data.ResourceID, nil
			}
			workspace, err := invoke("", "workspace", "create", "--path", project, "--operation-id", "scenario-workspace")
			if err != nil {
				return err
			}
			e.Setenv("WS", workspace)
			session, err := invoke("", "session", "create", "--workspace", workspace, "--name", "Scenario", "--operation-id", "scenario-session")
			if err != nil {
				return err
			}
			e.Setenv("SESSION", session)
			scoped := func(args ...string) []string { return append(args, "--workspace", workspace, "--session", session) }
			private := filepath.Join(e.WorkDir, "credentials")
			if err := stack.MkdirPrivate(private); err != nil {
				return err
			}
			for _, actor := range []string{"ORCH", "WORKER"} {
				file := filepath.Join(private, actor+".json")
				e.Setenv(actor, file)
				id, err := invoke("", scoped("agent", "register", "--name", strings.ToLower(actor), "--new-credential-file", file, "--operation-id", "scenario-register-"+actor)...)
				if err != nil {
					return err
				}
				e.Setenv(actor+"_ID", id)
			}
			if _, err := invoke("", scoped("session", "appoint", "--agent", e.Getenv("ORCH_ID"), "--expected-revision", "1", "--operation-id", "scenario-appoint")...); err != nil {
				return err
			}
			task, err := invoke("ORCH", scoped("task", "create", "--title", "Scenario task", "--assign", e.Getenv("WORKER_ID"), "--operation-id", "scenario-task")...)
			if err != nil {
				return err
			}
			e.Setenv("TASK", task)
			if _, err := invoke("WORKER", scoped("agent", "attach", "--agent", e.Getenv("WORKER_ID"), "--runtime-id", "scenario-runtime", "--expected-revision", "1", "--operation-id", "scenario-attach")...); err != nil {
				return err
			}
			other, err := invoke("", "session", "create", "--workspace", workspace, "--name", "Other", "--operation-id", "scenario-other")
			if err != nil {
				return err
			}
			e.Setenv("OTHER_SESSION", other)
			otherProject := filepath.Join(e.WorkDir, "other-project")
			if err := os.Mkdir(otherProject, 0700); err != nil {
				return err
			}
			otherWorkspace, err := invoke("", "workspace", "create", "--path", otherProject, "--operation-id", "scenario-other-workspace")
			if err != nil {
				return err
			}
			e.Setenv("OTHER_WS", otherWorkspace)
			e.Values["env"] = e.Vars
			e.Values["service"] = service
			return nil
		},
		Cmds: map[string]func(*testscript.TestScript, bool, []string){
			"as": func(ts *testscript.TestScript, neg bool, args []string) {
				if len(args) < 2 {
					ts.Fatalf("as OPERATOR|ORCH|WORKER command...")
				}
				actor := args[0]
				argv := append([]string{}, args[1:]...)
				argv = append(argv, "--state-dir", ts.Getenv("STATE"), "--workspace", ts.Getenv("WS"), "--session", ts.Getenv("SESSION"))
				if actor != "OPERATOR" {
					argv = append(argv, "--as", "agent", "--credential-file", ts.Getenv(actor))
				}
				err := ts.Exec(ts.Getenv("BIN"), argv...)
				if (err != nil) != neg {
					ts.Fatalf("command success=%v, want success=%v", err == nil, !neg)
				}
			},
			"event": func(ts *testscript.TestScript, neg bool, args []string) {
				if neg || (len(args) != 4 && len(args) != 5) {
					ts.Fatalf("event claude|codex|pi|opencode EVENT CWD SESSION [SUBAGENT]")
				}
				switch args[0] {
				case "claude", "codex", "pi", "opencode":
				default:
					ts.Fatalf("unknown harness")
				}
				ts.Setenv("HARNESS", args[0])
				input := map[string]any{"hook_event_name": args[1], "cwd": args[2], "session_id": args[3]}
				if len(args) == 5 {
					input["agent_id"] = args[4]
				}
				data, err := json.Marshal(input)
				if err != nil {
					ts.Fatalf("event: %v", err)
				}
				if err := os.WriteFile(filepath.Join(ts.Getenv("WORK"), "event.json"), data, 0600); err != nil {
					ts.Fatalf("event: %v", err)
				}
			},
			"hook": func(ts *testscript.TestScript, neg bool, args []string) {
				if neg || len(args) != 0 {
					ts.Fatalf("hook takes stdin event JSON")
				}
				err := ts.Exec(ts.Getenv("BIN"), scenarioHookArgs(ts)...)
				if err != nil {
					ts.Fatalf("hook failed: %v", err)
				}
			},
			"concurrent-hooks": func(ts *testscript.TestScript, neg bool, args []string) {
				if neg || len(args) != 0 {
					ts.Fatalf("concurrent-hooks takes no arguments")
				}
				var wg sync.WaitGroup
				outputs := make([][]byte, 2)
				errors := make([]error, 2)
				input := ts.ReadFile(filepath.Join(ts.Getenv("WORK"), "event.json"))
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				for i := range 2 {
					wg.Go(func() {
						cmd := exec.CommandContext(ctx, ts.Getenv("BIN"), scenarioHookArgs(ts)...)
						cmd.Env, cmd.Stdin = ts.Value("env").([]string), strings.NewReader(input)
						outputs[i], errors[i] = cmd.Output()
					})
				}
				wg.Wait()
				notices := 0
				for i, output := range outputs {
					if errors[i] != nil {
						ts.Fatalf("hook process failed")
					}
					if bytes.Contains(output, []byte("new-canary")) {
						ts.Fatalf("nudge exposed mail body")
					}
					notices += bytes.Count(output, []byte("New Task mail"))
				}
				if notices != 1 {
					ts.Fatalf("concurrent callbacks produced %d nudges, want one", notices)
				}
			},
			"age-hook-cache": func(ts *testscript.TestScript, neg bool, args []string) {
				if neg || len(args) != 0 {
					ts.Fatalf("age-hook-cache takes no arguments")
				}
				// Advance only fixture mtimes; no wall-clock sleep or production test knob.
				root := ts.Getenv("XDG_CACHE_HOME")
				if runtime.GOOS == "windows" {
					root = ts.Getenv("LOCALAPPDATA")
				} else if runtime.GOOS == "darwin" {
					root = filepath.Join(ts.Getenv("HOME"), "Library", "Caches")
				}
				err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if !entry.IsDir() && strings.HasSuffix(path, ".json") {
						old := time.Now().Add(-time.Minute)
						return os.Chtimes(path, old, old)
					}
					return nil
				})
				if err != nil {
					ts.Fatalf("age cache: %v", err)
				}
			},
			"seed-mail": func(ts *testscript.TestScript, neg bool, args []string) {
				if neg || len(args) > 1 {
					ts.Fatalf("seed-mail [COUNT]")
				}
				count := 105
				if len(args) == 1 {
					if _, err := fmt.Sscan(args[0], &count); err != nil || count < 1 || count > 3000 {
						ts.Fatalf("count must be 1..3000")
					}
				}
				s := ts.Value("service").(*stack.Service)
				cred, err := stack.ReadCredential(ts.Getenv("WORKER"))
				if err != nil {
					ts.Fatalf("fixture credential unavailable")
				}
				for i := 1; i <= count; i++ {
					_, err := s.Store.SendMail(context.Background(), cred.Token, stack.MailSendRequest{Context: stack.Context{WorkspaceID: ts.Getenv("WS"), SessionID: ts.Getenv("SESSION")}, TaskID: ts.Getenv("TASK"), OperationID: fmt.Sprintf("scenario-mail-%03d", i), Body: fmt.Sprintf("message-%03d", i)})
					if err != nil {
						ts.Fatalf("seed message %d: %v", i, err)
					}
				}
			},
			"legacy-hook": func(ts *testscript.TestScript, neg bool, args []string) {
				if neg || len(args) != 0 {
					ts.Fatalf("legacy-hook takes no arguments")
				}
				file := filepath.Join(ts.Getenv("CLAUDE_CONFIG_DIR"), "settings.json")
				var settings map[string]any
				if json.Unmarshal([]byte(ts.ReadFile(file)), &settings) != nil {
					ts.Fatalf("invalid settings fixture")
				}
				hooks := settings["hooks"].(map[string]any)
				command := quotePath(ts.Getenv("BIN")) + " hook --credential-file " + quotePath(ts.Getenv("WORKER")) + " --worktree " + quotePath(ts.Getenv("HOME"))
				hooks["SessionStart"] = append(hooks["SessionStart"].([]any), map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}})
				data, err := json.Marshal(settings)
				if err != nil {
					ts.Fatalf("encode legacy settings")
				}
				if os.WriteFile(file, data, 0600) != nil {
					ts.Fatalf("write legacy settings")
				}
			},
			"mcp-denied": func(ts *testscript.TestScript, neg bool, args []string) {
				if neg || len(args) != 0 {
					ts.Fatalf("mcp-denied takes no arguments")
				}
				var input bytes.Buffer
				encoder := json.NewEncoder(&input)
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": mcpInit()})
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
				for i, name := range []string{"mail_read", "mail_send"} {
					arguments := map[string]any{"task_id": ts.Getenv("TASK")}
					if name == "mail_send" {
						arguments["body"], arguments["operation_id"] = "retained", "scenario-original"
					}
					_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": i + 2, "method": "tools/call", "params": map[string]any{"name": name, "arguments": arguments}})
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, ts.Getenv("BIN"), "mcp", "--state-dir", ts.Getenv("STATE"), "--credential-file", ts.Getenv("WORKER"))
				cmd.Env, cmd.Stdin = ts.Value("env").([]string), &input
				output, err := cmd.Output()
				if err != nil {
					ts.Fatalf("MCP process failed: %v", err)
				}
				decoder := json.NewDecoder(bytes.NewReader(output))
				for i := 0; i < 3; i++ {
					var reply struct {
						Result struct {
							IsError bool `json:"isError"`
							Content []struct {
								Text string `json:"text"`
							} `json:"content"`
						} `json:"result"`
					}
					if decoder.Decode(&reply) != nil {
						ts.Fatalf("missing MCP reply")
					}
					if i > 0 && (!reply.Result.IsError || len(reply.Result.Content) != 1 || !strings.Contains(reply.Result.Content[0].Text, "forbidden")) {
						ts.Fatalf("removed actor retained MCP mail authority")
					}
				}
			},
			"scope-cursors": func(ts *testscript.TestScript, neg bool, args []string) {
				if neg || len(args) != 0 {
					ts.Fatalf("scope-cursors takes no arguments")
				}
				s := ts.Value("service").(*stack.Service)
				cred, err := stack.ReadCredential(ts.Getenv("WORKER"))
				if err != nil {
					ts.Fatalf("fixture credential unavailable")
				}
				ref := stack.Context{WorkspaceID: ts.Getenv("WS"), SessionID: ts.Getenv("SESSION")}
				task := ts.Getenv("TASK")
				page, err := s.Store.ReadMail(context.Background(), cred.Token, stack.MailReadRequest{Context: ref, TaskID: task})
				if err != nil || len(page.Messages) == 0 {
					ts.Fatalf("missing own mail")
				}
				for _, foreign := range []stack.Context{{WorkspaceID: ref.WorkspaceID, SessionID: ts.Getenv("OTHER_SESSION")}, {WorkspaceID: ts.Getenv("OTHER_WS"), SessionID: ref.SessionID}} {
					if _, err := s.Store.ReadMail(context.Background(), cred.Token, stack.MailReadRequest{Context: foreign, TaskID: task, Cursor: page.Cursor}); err == nil {
						ts.Fatalf("foreign context read succeeded")
					}
					if _, err := s.Store.SendMail(context.Background(), cred.Token, stack.MailSendRequest{Context: foreign, TaskID: task, OperationID: "scenario-send", Body: "scope-canary"}); err == nil {
						ts.Fatalf("foreign context replay succeeded")
					}
					if _, err := s.Store.CheckMail(context.Background(), cred.Token, stack.MailCheckRequest{Context: foreign}); err == nil {
						ts.Fatalf("foreign context scan succeeded")
					}
				}
				// A syntactically valid cursor for a different Task must not reset to zero.
				foreignCursor := stack.MailCursor(ts.Getenv("WORKER_ID"), 1)
				if _, err := s.Store.ReadMail(context.Background(), cred.Token, stack.MailReadRequest{Context: ref, TaskID: task, Cursor: foreignCursor}); err == nil {
					ts.Fatalf("foreign Task cursor accepted")
				}
			},
			"mail-boundary": func(ts *testscript.TestScript, neg bool, args []string) {
				if neg || len(args) != 0 {
					ts.Fatalf("mail-boundary takes no arguments")
				}
				s := ts.Value("service").(*stack.Service)
				cred, err := stack.ReadCredential(ts.Getenv("WORKER"))
				if err != nil {
					ts.Fatalf("fixture credential unavailable")
				}
				ref := stack.Context{WorkspaceID: ts.Getenv("WS"), SessionID: ts.Getenv("SESSION")}
				body := strings.Repeat("\"\\\n", stack.MaxMailBodyBytes/3) + "x"
				request := stack.MailSendRequest{Context: ref, TaskID: ts.Getenv("TASK"), OperationID: "scenario-invalid-mention", Body: body, Mentions: []string{"WORKER", "missing"}}
				if _, err := s.Store.SendMail(context.Background(), cred.Token, request); err == nil {
					ts.Fatalf("invalid recipient accepted")
				}
				page, err := s.Store.ReadMail(context.Background(), cred.Token, stack.MailReadRequest{Context: ref, TaskID: request.TaskID})
				if err != nil || len(page.Messages) != 0 {
					ts.Fatalf("invalid send published content")
				}
				request.OperationID, request.Mentions = "scenario-boundary-send", nil
				receipt, err := s.Store.SendMail(context.Background(), cred.Token, request)
				if err != nil {
					ts.Fatalf("maximum body rejected: %v", err)
				}
				retry, err := s.Store.SendMail(context.Background(), cred.Token, request)
				if err != nil || retry != receipt {
					ts.Fatalf("boundary replay changed receipt")
				}
				page, err = s.Store.ReadMail(context.Background(), cred.Token, stack.MailReadRequest{Context: ref, TaskID: request.TaskID})
				if err != nil || len(page.Messages) != 1 || page.Messages[0].Body != body || page.More {
					ts.Fatalf("boundary content truncated or duplicated")
				}
			},
			"browser-peek": func(ts *testscript.TestScript, neg bool, args []string) {
				if neg || len(args) != 0 {
					ts.Fatalf("browser-peek takes no arguments")
				}
				s := ts.Value("service").(*stack.Service)
				operator, err := stack.ReadCredential(filepath.Join(ts.Getenv("STATE"), "operator.json"))
				if err != nil {
					ts.Fatalf("fixture operator unavailable")
				}
				ref := stack.Context{WorkspaceID: ts.Getenv("WS"), SessionID: ts.Getenv("SESSION")}
				code := strings.Repeat("A", 43)
				hash := sha256.Sum256([]byte(code))
				_, err = s.Store.OpenBrowser(context.Background(), operator.Token, stack.BrowserOpenRequest{Context: ref, OperationID: "scenario-browser-open", CredentialHash: hex.EncodeToString(hash[:])})
				if err != nil {
					ts.Fatalf("view issuance failed")
				}
				token, err := s.Store.RedeemViewLink(context.Background(), stack.ViewRedeemRequest{Context: ref, Code: code})
				if err != nil {
					ts.Fatalf("view redemption failed")
				}
				page, err := s.Store.ReadMail(context.Background(), token, stack.MailReadRequest{Context: ref, TaskID: ts.Getenv("TASK")})
				if err != nil || len(page.Messages) == 0 {
					ts.Fatalf("browser could not read its mail")
				}
				_, err = s.Store.ReadMail(context.Background(), token, stack.MailReadRequest{Context: stack.Context{WorkspaceID: ref.WorkspaceID, SessionID: ts.Getenv("OTHER_SESSION")}, TaskID: ts.Getenv("TASK")})
				if err == nil {
					ts.Fatalf("browser crossed Session boundary")
				}
			},
		},
	})
}

func scenarioHookArgs(ts *testscript.TestScript) []string {
	return []string{"hook", "--state-dir", ts.Getenv("STATE"), "--credential-file", ts.Getenv("WORKER"), "--workspace", ts.Getenv("WS"), "--session", ts.Getenv("SESSION"), "--agent", ts.Getenv("WORKER_ID"), "--worktree", ts.Getenv("PROJECT"), "--harness", ts.Getenv("HARNESS"), "--runtime-id", "scenario-runtime"}
}

func cmdErrorMessage(err error) string {
	if exit, ok := err.(*exec.ExitError); ok {
		var envelope struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(exit.Stderr, &envelope) == nil {
			return envelope.Error.Message
		}
	}
	return "no JSON diagnostic"
}

// Fail before release if test tooling enters any shipped dependency closure.
func TestProductionHasNoTestTooling(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./...")
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"github.com/rogpeppe/go-internal", "pgregory.net/rapid"} {
		if bytes.Contains(out, []byte(forbidden)) {
			t.Fatalf("test-only dependency in production: %s", forbidden)
		}
	}
}
