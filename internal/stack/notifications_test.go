package stack

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeHerdrConfig struct {
	Binding RuntimeBinding   `json:"binding"`
	Targets []RuntimeBinding `json:"targets,omitempty"`
	Status  string           `json:"status"`
	Mode    string           `json:"mode"`
}
type fakeHerdrCall struct{ Args, Env []string }

func TestHerdrExecutableHelper(t *testing.T) {
	split := -1
	for i, a := range os.Args {
		if a == "--" {
			split = i
			break
		}
	}
	if split < 0 {
		return
	}
	dir := os.Args[split+1]
	args := os.Args[split+2:]
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		os.Exit(17)
	}
	var config fakeHerdrConfig
	if json.Unmarshal(data, &config) != nil {
		os.Exit(18)
	}
	f, err := os.OpenFile(filepath.Join(dir, "calls.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(19)
	}
	_ = json.NewEncoder(f).Encode(fakeHerdrCall{Args: args, Env: os.Environ()})
	f.Close()
	if len(args) == 3 && args[0] == "status" && args[1] == "server" && args[2] == "--json" {
		// The release string is not checked; the protocol and compatibility are.
		status := map[string]any{"status": "running", "running": true, "version": "9.9.9", "protocol": herdrProtocol, "compatible": true, "endpoint_compatible": true, "socket": "must-not-be-retained"}
		switch config.Mode {
		case "version":
			status["protocol"] = herdrProtocol + 1
		case "incompatible":
			status["endpoint_compatible"] = false
		case "not_running":
			status = map[string]any{"status": "not_running", "running": false, "protocol": nil}
		}
		_ = json.NewEncoder(os.Stdout).Encode(status)
		os.Exit(0)
	}
	if len(args) >= 3 {
		for _, binding := range config.Targets {
			if args[2] == binding.HerdrAlias || args[2] == binding.PaneID {
				config.Binding = binding
				break
			}
		}
	}
	agent := map[string]any{"name": config.Binding.HerdrAlias, "pane_id": config.Binding.PaneID, "terminal_id": config.Binding.TerminalID, "tab_id": "w1:t2", "agent": "codex", "agent_status": config.Status, "agent_session": map[string]any{"agent": "codex", "kind": "id", "source": "herdr:codex", "value": config.Binding.NativeSessionID}, "cwd": "must-not-be-retained", "terminal_title": "must-not-be-retained"}
	// Herdr's Pi integration reports the session file's path, not an ID.
	switch config.Mode {
	case "pi", "pi_id", "pi_relative":
		session := map[string]any{"agent": "pi", "kind": "path", "source": "herdr:pi", "value": config.Binding.NativeSessionID}
		if config.Mode == "pi_id" {
			session["kind"] = "id"
		} else if config.Mode == "pi_relative" {
			session["value"] = "sessions/relative.jsonl"
		}
		agent["agent"], agent["agent_session"] = "pi", session
	}
	if len(args) == 3 && args[0] == "agent" && args[1] == "get" {
		switch config.Mode {
		case "mismatch":
			agent["terminal_id"] = "different-terminal"
		case "alias_reused":
			agent["name"] = "another-agent"
		case "pane_changed":
			agent["pane_id"] = "w1:p99"
		case "native_changed":
			agent["agent_session"].(map[string]any)["value"] = "replacement-native"
		case "kind_changed":
			agent["agent"] = "pi"
		case "remove_executable":
			_ = os.Remove(filepath.Join(dir, "herdr"))
		case "missing":
			delete(agent, "agent_session")
		case "malformed":
			fmt.Println("not JSON")
			os.Exit(0)
		case "large":
			fmt.Println(strings.Repeat("x", 40000))
			os.Exit(0)
		case "get_wait":
			fakeHerdrWait(dir, "get-started")
		case "unnamed", "rename_taken":
			delete(agent, "name")
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"result": map[string]any{"agent": agent}})
		os.Exit(0)
	}
	// herdr focus: agent focus by pane, else tab focus by the reported tab.
	if len(args) == 3 && args[1] == "focus" && (args[0] == "agent" && args[2] == config.Binding.PaneID || args[0] == "tab" && args[2] == "w1:t2") {
		if config.Mode == "focus_fails" && args[0] == "agent" {
			fmt.Fprintln(os.Stderr, `{"error":{"code":"internal","message":"private raw diagnostic"}}`)
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"result": map[string]any{"agent": agent}})
		os.Exit(0)
	}
	// herdr bind names its own unnamed pane; Herdr refuses a live name in use.
	if len(args) == 4 && args[0] == "agent" && args[1] == "rename" && args[2] == config.Binding.PaneID && herdrName.MatchString(args[3]) {
		if config.Mode == "rename_taken" {
			fmt.Fprintln(os.Stderr, `{"error":{"code":"agent_name_taken","message":"private raw diagnostic"}}`)
			os.Exit(1)
		}
		agent["name"] = args[3]
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"result": map[string]any{"agent": agent}})
		os.Exit(0)
	}
	if len(args) >= 2 && args[0] == "agent" && args[1] == "prompt" && len(args) != 4 {
		fmt.Fprintln(os.Stderr, `{"error":{"code":"invalid_cli","message":"--timeout requires --wait; normal submission accepts exactly TARGET TEXT"}}`)
		os.Exit(2)
	}
	if len(args) == 4 && args[0] == "agent" && args[1] == "prompt" {
		switch config.Mode {
		case "blocked":
			_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"error": map[string]string{"code": "agent_blocked", "message": "private raw diagnostic"}})
			os.Exit(1)
		case "error":
			fmt.Fprintln(os.Stderr, `{"error":{"code":"timeout","message":"private raw diagnostic"}}`)
			os.Exit(1)
		case "hang":
			time.Sleep(30 * time.Second)
		case "prompt_wait":
			fakeHerdrWait(dir, "prompt-started")
		case "crash_owner":
			_ = os.WriteFile(filepath.Join(dir, "input-submitted"), []byte("synthetic input boundary"), 0600)
			p, _ := os.FindProcess(os.Getppid())
			_ = p.Kill()
			os.Exit(0)
		case "bad_success":
			fmt.Println("not JSON")
			os.Exit(0)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"result": map[string]any{"agent": agent}})
		os.Exit(0)
	}
	os.Exit(20)
}
func fakeHerdrWait(dir, marker string) {
	_ = os.WriteFile(filepath.Join(dir, marker), nil, 0600)
	for i := 0; i < 500; i++ {
		if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	os.Exit(21)
}

type noticeFixture struct {
	mailTeam
	n      *notifier
	dir    string
	config fakeHerdrConfig
}

func noticeTestFixture(t *testing.T) *noticeFixture {
	t.Helper()
	unixOnly(t, "the fake Herdr is a shell script, and a-stack refuses Herdr on Windows")
	f := mailFixture(t)
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	// The adapter scrubs the child environment. Without GOCOVERDIR a coverage
	// build appends a warning to stderr, which turns known errors uncertain.
	coverage := ""
	if coverDir := os.Getenv("GOCOVERDIR"); coverDir != "" {
		coverage = "GOCOVERDIR=" + quote(coverDir) + " "
	}
	wrapper := filepath.Join(dir, "herdr")
	if err = os.WriteFile(wrapper, []byte("#!/bin/sh\nGORACE=atexit_sleep_ms=0 "+coverage+"exec "+quote(executable)+" -test.run=^TestHerdrExecutableHelper$ -- "+quote(dir)+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	binding := RuntimeBinding{InstanceID: "fixture-instance", HerdrAlias: "fixture-b", Endpoint: filepath.Join(dir, "recorded.sock"), PaneID: "w1:p2", TerminalID: "terminal-2", NativeSessionID: "native-2", Harness: "codex", HarnessVersion: "0.164.0"}
	_, err = f.service.Store.BindAgent(ctx, f.b.Token, "attach", AgentBindingRequest{Context: f.ref(0), OperationID: opaqueID(), AgentID: f.bID, ExpectedRevision: 1, Runtime: &binding})
	if err != nil {
		t.Fatal(err)
	}
	h := newHerdr(wrapper)
	h.verified = func(b RuntimeBinding) bool { return b.Harness == "codex" && b.HarnessVersion == "0.164.0" }
	result := &noticeFixture{mailTeam: f, n: newNotifier(f.service.Store, h), dir: dir, config: fakeHerdrConfig{Binding: binding, Status: "working"}}
	result.write(t)
	return result
}
func (f *noticeFixture) write(t *testing.T) {
	t.Helper()
	b, _ := json.Marshal(f.config)
	if err := os.WriteFile(filepath.Join(f.dir, "config.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}
func (f *noticeFixture) calls(t *testing.T) []fakeHerdrCall {
	t.Helper()
	file, err := os.Open(filepath.Join(f.dir, "calls.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var calls []fakeHerdrCall
	s := bufio.NewScanner(file)
	for s.Scan() {
		var c fakeHerdrCall
		if json.Unmarshal(s.Bytes(), &c) != nil {
			t.Fatal("bad fixture log")
		}
		calls = append(calls, c)
	}
	if s.Err() != nil {
		t.Fatal(s.Err())
	}
	return calls
}
func (f *noticeFixture) prompts(t *testing.T) int {
	t.Helper()
	n := 0
	for _, c := range f.calls(t) {
		if len(c.Args) > 1 && c.Args[1] == "prompt" {
			n++
		}
	}
	return n
}
func (f *noticeFixture) wait(t *testing.T, marker string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(f.dir, marker)); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("fixture did not reach controlled boundary")
}
func (f *noticeFixture) release(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
}
func (f *noticeFixture) drain(t *testing.T, limit int) NoticeDrainResult {
	t.Helper()
	r, err := f.n.drain(ctx, "", limit)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNotificationsExactBurstBatchAndEndpoint(t *testing.T) {
	f := noticeTestFixture(t)
	t.Setenv("HERDR_SESSION", "wrong-session")
	t.Setenv("HERDR_PANE_ID", "wrong-pane")
	t.Setenv("HERDR_SOCKET_PATH", "/wrong.sock")
	t.Setenv("UNRELATED_CREDENTIAL", "must-not-inherit")
	for i := 0; i < 5; i++ {
		f.send(t, f.a.Token, "Hello @03-worker")
	}
	other := f.create(t, "Another Task")
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: other.ResourceID, ExpectedRevision: 1, AgentID: f.aID})
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: other.ResourceID, ExpectedRevision: 2, AgentID: f.bID})
	if _, err := f.service.Store.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: other.ResourceID, Body: "Other Task @03-worker", OperationID: opaqueID()}); err != nil {
		t.Fatal(err)
	}
	if r := f.drain(t, 4); r.Submitted != 1 || r.Checked != 1 {
		t.Fatal("burst did not coalesce", r)
	}
	s := f.service.Store
	if countTest(t, s, "SELECT count(*) FROM notice_batches WHERE state='submitted'") != 1 || countTest(t, s, "SELECT count(*) FROM notice_batch_refs") != 6 || countTest(t, s, "SELECT count(*) FROM mail_mentions WHERE notice_state='submitted'") != 6 {
		t.Fatal("exact batch references")
	}
	if r := f.drain(t, 4); r.Checked != 0 || f.prompts(t) != 1 {
		t.Fatal("submitted batch repeated")
	}
	for _, call := range f.calls(t) {
		env := map[string]string{}
		for _, value := range call.Env {
			parts := strings.SplitN(value, "=", 2)
			env[parts[0]] = parts[1]
		}
		if env["HERDR_SOCKET_PATH"] != f.config.Binding.Endpoint || env["HERDR_SESSION"] != "" || env["HERDR_PANE_ID"] != "" || env["UNRELATED_CREDENTIAL"] != "" {
			t.Fatal("inherited selector/credential or wrong endpoint")
		}
		if len(call.Args) > 1 && call.Args[1] == "prompt" {
			want := []string{"agent", "prompt", f.config.Binding.PaneID, noticeText}
			a, _ := json.Marshal(call.Args)
			b, _ := json.Marshal(want)
			if string(a) != string(b) || len(call.Args[3]) > 256 || strings.Contains(call.Args[3], f.taskID) {
				t.Fatal("unexpected prompt argv/payload")
			}
		}
	}
}
func TestNotificationsArrivalAndRecipientSerialization(t *testing.T) {
	f := noticeTestFixture(t)
	f.config.Mode = "prompt_wait"
	f.write(t)
	f.send(t, f.a.Token, "First @03-worker")
	result := make(chan error, 1)
	go func() { _, err := f.n.drain(ctx, "", 1); result <- err }()
	f.wait(t, "prompt-started")
	f.send(t, f.a.Token, "Second @03-worker")
	f.send(t, f.a.Token, "Third @03-worker")
	if r := f.drain(t, 4); !r.Busy {
		t.Fatal("overlapping recipient drain")
	}
	if countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='pending'") != 2 {
		t.Fatal("arrivals captured by old batch")
	}
	f.release(t)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='submitted'") != 1 {
		t.Fatal("broad completion consumed arrivals")
	}
	if r := f.drain(t, 4); r.Submitted != 1 || f.prompts(t) != 2 || countTest(t, f.service.Store, "SELECT count(*) FROM notice_batch_refs") != 3 {
		t.Fatal("arrival batch lost or repeated")
	}
}
func TestNotificationsPreinputBackoffAndUncertain(t *testing.T) {
	for _, mode := range []string{"blocked", "error", "bad_success", "hang"} {
		t.Run(mode, func(t *testing.T) {
			f := noticeTestFixture(t)
			f.config.Mode = mode
			f.write(t)
			if mode == "hang" {
				f.n.adapter.timeout = 100 * time.Millisecond
			}
			clock := time.Now()
			f.n.clock = func() time.Time { return clock }
			f.send(t, f.a.Token, "Notice @03-worker")
			r := f.drain(t, 1)
			if f.prompts(t) != 1 {
				t.Fatal("fixture never submitted")
			}
			if mode == "blocked" {
				if r.Pending != 1 || countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='pending'") != 1 {
					t.Fatal("known preinput failure lost")
				}
				if f.drain(t, 1).Checked != 0 {
					t.Fatal("tight preinput retry")
				}
				clock = clock.Add(5 * time.Second)
				f.config.Mode = ""
				f.write(t)
				if f.drain(t, 1).Submitted != 1 || f.prompts(t) != 2 {
					t.Fatal("known preinput failure unrecoverable")
				}
			} else {
				if r.Uncertain != 1 || countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='uncertain'") != 1 {
					t.Fatal("postinput uncertainty not durable")
				}
				clock = clock.Add(time.Minute)
				if f.drain(t, 4).Checked != 0 || f.prompts(t) != 1 {
					t.Fatal("uncertain automatically repeated")
				}
			}
		})
	}
}
func TestNotificationsUnavailableMetadataAndVersion(t *testing.T) {
	for _, mode := range []string{"mismatch", "alias_reused", "pane_changed", "native_changed", "kind_changed", "missing", "malformed", "large", "version", "incompatible", "not_running", "blocked-state", "unknown", "stopped", "unsupported", "incomplete", "absent"} {
		t.Run(mode, func(t *testing.T) {
			f := noticeTestFixture(t)
			f.config.Mode = mode
			switch mode {
			case "blocked-state":
				f.config.Status = "blocked"
			case "unknown":
				f.config.Status = "unknown"
			case "stopped":
				f.config.Status = "stopped"
			case "unsupported":
				f.n.adapter.verified = func(RuntimeBinding) bool { return false }
			case "incomplete":
				execTest(t, f.service.Store, "UPDATE agents SET binding='{}' WHERE id=?", f.bID)
			case "absent":
				f.n.adapter.executable = filepath.Join(f.dir, "not-installed")
			}
			f.write(t)
			f.send(t, f.a.Token, "Private body @03-worker")
			if r := f.drain(t, 4); r.Pending != 1 || f.prompts(t) != 0 || countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='pending'") != 1 || countTest(t, f.service.Store, "SELECT count(*) FROM notice_batches") != 0 {
				t.Fatal("unavailable target prompted/lost mail")
			}
			if countTest(t, f.service.Store, "SELECT count(*) FROM notice_targets WHERE reason!=''") != 1 || f.drain(t, 1).Checked != 0 {
				t.Fatal("unavailable diagnostics/backoff")
			}
			task := f.show(t, f.taskID)
			if task.Progress != "waiting" || task.NeedsYou {
				t.Fatal("runtime altered Task state")
			}
		})
	}
}
func TestNotificationsMembershipClosureAndBindingBeforeInput(t *testing.T) {
	for _, change := range []string{"remove", "close", "rebind"} {
		t.Run(change, func(t *testing.T) {
			f := noticeTestFixture(t)
			f.config.Mode = "get_wait"
			f.write(t)
			f.send(t, f.a.Token, "Waiting @03-worker")
			result := make(chan error, 1)
			go func() { _, err := f.n.drain(ctx, "", 1); result <- err }()
			f.wait(t, "get-started")
			switch change {
			case "remove":
				f.change(t, f.orch.Token, "unassign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 3, AgentID: f.bID})
			case "close":
				_, err := f.service.Store.ChangeSession(ctx, f.operator.Token, "close", SessionChangeRequest{Context: f.ref(0), OperationID: opaqueID(), ExpectedRevision: 2})
				if err != nil {
					t.Fatal(err)
				}
			case "rebind":
				binding := f.config.Binding
				binding.NativeSessionID = "replacement"
				_, err := f.service.Store.BindAgent(ctx, f.operator.Token, "rebind", AgentBindingRequest{Context: f.ref(0), OperationID: opaqueID(), AgentID: f.bID, ExpectedRevision: 2, Runtime: &binding, CredentialHash: tokenHash(opaqueID())})
				if err != nil {
					t.Fatal(err)
				}
			}
			f.release(t)
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			if f.prompts(t) != 0 || countTest(t, f.service.Store, "SELECT count(*) FROM notice_batches") != 0 {
				t.Fatal("preinput authority change bypassed")
			}
		})
	}
}
func TestNotificationsSuppressionSurvivesInflightOutcomes(t *testing.T) {
	for _, state := range []NoticeState{NoticeSubmitted, NoticeUncertain, NoticePending} {
		t.Run(string(state), func(t *testing.T) {
			f := noticeTestFixture(t)
			f.send(t, f.a.Token, "Removed @03-worker")
			target, err := f.n.nextTarget(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			batch, err := f.n.claim(ctx, target)
			if err != nil {
				t.Fatal(err)
			}
			f.change(t, f.orch.Token, "unassign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 3, AgentID: f.bID})
			f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 4, AgentID: f.bID})
			if err = f.n.finish(batch, state, "fixture_outcome"); err != nil {
				t.Fatal(err)
			}
			if countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='suppressed'") != 1 || countTest(t, f.service.Store, "SELECT count(*) FROM notice_batches WHERE state='suppressed'") != 1 {
				t.Fatal("terminal suppression overwritten")
			}
			f.send(t, f.a.Token, "New @03-worker")
			if f.drain(t, 4).Submitted != 1 || f.prompts(t) != 1 {
				t.Fatal("rejoin new mentions failed")
			}
		})
	}
}
func TestNotificationsExternalCommandDoesNotHoldTransaction(t *testing.T) {
	f := noticeTestFixture(t)
	f.config.Mode = "prompt_wait"
	f.write(t)
	f.send(t, f.a.Token, "Inflight @03-worker")
	var wg sync.WaitGroup
	wg.Add(1)
	result := make(chan error, 1)
	go func() { defer wg.Done(); _, err := f.n.drain(ctx, "", 1); result <- err }()
	f.wait(t, "prompt-started")
	f.change(t, f.orch.Token, "unassign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 3, AgentID: f.bID})
	f.release(t)
	wg.Wait()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='suppressed'") != 1 {
		t.Fatal("inflight removal resurrected")
	}
}

type noticeCrashFixture struct{ State, Executable string }

func TestNotificationsCrashHelper(t *testing.T) {
	path := os.Getenv("ASTACK_NOTICE_CRASH_FIXTURE")
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		os.Exit(30)
	}
	var fixture noticeCrashFixture
	if json.Unmarshal(data, &fixture) != nil {
		os.Exit(31)
	}
	service, err := Start(context.Background(), fixture.State, "127.0.0.1:0")
	if err != nil {
		os.Exit(32)
	}
	h := newHerdr(fixture.Executable)
	h.verified = func(RuntimeBinding) bool { return true }
	_, _ = newNotifier(service.Store, h).drain(context.Background(), "", 1)
	os.Exit(33)
}
func TestNotificationsCrashRecoveryNeverResendsCapturedRefs(t *testing.T) {
	f := noticeTestFixture(t)
	f.config.Mode = "crash_owner"
	f.write(t)
	f.send(t, f.a.Token, "Before crash @03-worker")
	state := f.service.lock.dir
	if err := f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(f.dir, "crash.json")
	data, _ := json.Marshal(noticeCrashFixture{State: state, Executable: f.n.adapter.executable})
	if err := os.WriteFile(fixturePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(exe, "-test.run=^TestNotificationsCrashHelper$")
	command.Env = append(os.Environ(), "ASTACK_NOTICE_CRASH_FIXTURE="+fixturePath)
	if err = command.Run(); err == nil {
		t.Fatal("fixture did not crash")
	}
	if _, err = os.Stat(filepath.Join(f.dir, "input-submitted")); err != nil {
		t.Fatal("crash not after simulated input")
	}
	restored, err := Start(ctx, state, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close(ctx)
	f.n.store = restored.Store
	if countTest(t, restored.Store, "SELECT count(*) FROM notice_batches WHERE state='uncertain' AND reason='recovery_uncertain'") != 1 || countTest(t, restored.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='uncertain'") != 1 {
		t.Fatal("inflight restart not uncertain")
	}
	f.config.Mode = ""
	f.write(t)
	if f.drain(t, 4).Checked != 0 || f.prompts(t) != 1 {
		t.Fatal("crashed covered refs repeated")
	}
	if _, err = restored.Store.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: f.taskID, Body: "After crash @03-worker", OperationID: opaqueID()}); err != nil {
		t.Fatal(err)
	}
	if f.drain(t, 4).Submitted != 1 || f.prompts(t) != 2 || countTest(t, restored.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='uncertain'") != 1 {
		t.Fatal("new refs after uncertainty failed")
	}
	var integrity string
	if restored.Store.db.QueryRow("PRAGMA integrity_check").Scan(&integrity) != nil || integrity != "ok" {
		t.Fatal("crash database integrity")
	}
}

func TestNotificationsHTTPPrivacyAndAutomaticWake(t *testing.T) {
	f := noticeTestFixture(t)
	service := f.service
	own, err := service.notificationStatus(ctx, f.b.Token, NoticeRequest{Context: f.ref(0)})
	if err != nil || own.Enabled || len(own.Targets) != 1 || own.Targets[0].AgentID != f.bID || own.Targets[0].Reason != "integration_disabled" {
		t.Fatal("disabled own status", err)
	}
	_, err = service.notificationStatus(ctx, f.a.Token, NoticeRequest{Context: f.ref(0), AgentID: f.bID})
	requireCode(t, err, "forbidden")
	operatorClient := clientForCredential(t, f.taskTeam, f.operator)
	_, err = operatorClient.Do(ctx, "POST", "/v1/notifications/drain", NoticeRequest{Context: f.ref(0)})
	requireCode(t, err, "integration_unavailable")
	workerClient := clientForCredential(t, f.taskTeam, f.b)
	_, err = workerClient.Do(ctx, "POST", "/v1/notifications/drain", NoticeRequest{Context: f.ref(0)})
	requireCode(t, err, "forbidden")
	browser := issueTest(t, service.Store, BrowserCapability, opaqueID(), f.ref(0).SessionID)
	browserClient := clientForCredential(t, f.taskTeam, browser)
	_, err = browserClient.Do(ctx, "POST", "/v1/notifications/status", NoticeRequest{Context: f.ref(0)})
	requireCode(t, err, "forbidden")
	if err = service.enableNotifications(f.n.adapter); err != nil {
		t.Fatal(err)
	}
	f.send(t, f.a.Token, "Wake promptly @03-worker")
	deadline := time.Now().Add(5 * time.Second)
	for countTest(t, service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='submitted'") != 1 {
		if time.Now().After(deadline) {
			t.Fatal("postcommit wake did not drain")
		}
		time.Sleep(5 * time.Millisecond)
	}
	data, err := workerClient.Do(ctx, "POST", "/v1/notifications/status", NoticeRequest{Context: f.ref(0)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), f.config.Binding.Endpoint) || strings.Contains(string(data), f.config.Binding.NativeSessionID) || strings.Contains(string(data), "Wake promptly") || strings.Contains(string(data), "must-not-be-retained") {
		t.Fatal("diagnostic leaked private data")
	}
	var result struct {
		Data NoticeStatus `json:"data"`
	}
	if json.Unmarshal(data, &result) != nil || !result.Data.Enabled || result.Data.Targets[0].Counts[NoticeSubmitted] != 1 {
		t.Fatal("delivery state absent")
	}
}
func TestNotificationsV4UpgradeAndRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			f := noticeTestFixture(t)
			f.send(t, f.a.Token, "Retained @03-worker")
			path := f.service.Store.path
			state := f.service.lock.dir
			if err := f.service.Close(ctx); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(undoMigration7 + undoMigration6 + "DROP TABLE notice_batch_refs; DROP TABLE notice_batches; DROP TABLE notice_targets; PRAGMA user_version=4"); err != nil {
				t.Fatal(err)
			}
			if fail {
				if _, err = db.Exec("CREATE TABLE notice_targets(sentinel TEXT); INSERT INTO notice_targets VALUES('retain')"); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()
			if fail {
				if s, err := Start(ctx, state, "127.0.0.1:0"); err == nil {
					s.Close(ctx)
					t.Fatal("colliding migration accepted")
				}
				db, err = sql.Open("sqlite3", path)
				if err != nil {
					t.Fatal(err)
				}
				var version, count int
				var sentinel string
				if db.QueryRow("PRAGMA user_version").Scan(&version) != nil || version != 4 || db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='notice_batches'").Scan(&count) != nil || count != 0 || db.QueryRow("SELECT sentinel FROM notice_targets").Scan(&sentinel) != nil || sentinel != "retain" {
					t.Fatal("migration rollback lost original schema")
				}
				if _, err = db.Exec("DROP TABLE notice_targets"); err != nil {
					t.Fatal(err)
				}
				db.Close()
			}
			service, err := Start(ctx, state, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close(ctx)
			page, err := service.Store.ReadMail(ctx, f.b.Token, MailReadRequest{Context: f.ref(0), TaskID: f.taskID})
			if err != nil || len(page.Messages) != 1 || countTest(t, service.Store, "PRAGMA user_version") != schemaVersion {
				t.Fatal("mail/credentials lost during upgrade", err)
			}
		})
	}
}

func TestNotificationsFailedOutcomeCommitRecoversWithoutResubmission(t *testing.T) {
	f := noticeTestFixture(t)
	f.send(t, f.a.Token, "Outcome write @03-worker")
	execTest(t, f.service.Store, `CREATE TRIGGER fail_notice_outcome BEFORE UPDATE OF state ON notice_batches WHEN NEW.state='submitted' BEGIN SELECT RAISE(ABORT,'fixture failure'); END`)
	if _, err := f.n.drain(ctx, "", 1); err == nil {
		t.Fatal("failed storage reported successful delivery")
	}
	if countTest(t, f.service.Store, "SELECT count(*) FROM notice_batches WHERE state='submitting'") != 1 {
		t.Fatal("failure did not preserve uncertain boundary")
	}
	execTest(t, f.service.Store, "DROP TRIGGER fail_notice_outcome")
	f.send(t, f.a.Token, "New after failed outcome @03-worker")
	if r := f.drain(t, 4); r.Submitted != 1 || f.prompts(t) != 2 || countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='uncertain'") != 1 {
		t.Fatal("outcome recovery duplicated captured refs or blocked arrivals")
	}
}

// Root reproduced this parser dependency against installed Herdr 0.9.5;
// tagged src/cli/agent.rs::agent_prompt rejects timeout without wait before input.
// This regression keeps the fake strict instead of accepting impossible argv.
func TestNotificationsHerdrPromptParserContract(t *testing.T) {
	f := noticeTestFixture(t)
	f.config.Mode = "prompt_wait"
	f.write(t)
	_, diagnostic, started, err := f.n.adapter.command(ctx, f.config.Binding, "agent", "prompt", f.config.Binding.PaneID, noticeText, "--timeout", "5000")
	var exit *exec.ExitError
	if !started || !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(diagnostic), "--timeout requires --wait") {
		t.Fatal("fake failed to enforce installed parser contract")
	}
	if _, err = os.Stat(filepath.Join(f.dir, "prompt-started")); !os.IsNotExist(err) {
		t.Fatal("invalid argv reached fake input boundary")
	}
	f.config.Mode = ""
	f.write(t)
	f.send(t, f.a.Token, "Correct normal argv @03-worker")
	if f.drain(t, 1).Submitted != 1 {
		t.Fatal("normal no-wait argv rejected")
	}
	calls := f.calls(t)
	last := calls[len(calls)-1]
	if len(last.Args) != 4 || last.Args[2] != f.config.Binding.PaneID || last.Args[3] != noticeText {
		t.Fatal("normal submission added unsupported wait/timeout options")
	}
}

func TestNotificationsExecutableDisappearsBeforeInput(t *testing.T) {
	f := noticeTestFixture(t)
	wrapper, err := os.ReadFile(f.n.adapter.executable)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Now()
	f.n.clock = func() time.Time { return clock }
	f.config.Mode = "remove_executable"
	f.write(t)
	f.send(t, f.a.Token, "Before input @03-worker")
	if r := f.drain(t, 1); r.Pending != 1 || r.Uncertain != 0 || f.prompts(t) != 0 || countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='pending'") != 1 {
		t.Fatal("process start failure became uncertain/lost")
	}
	if err = os.WriteFile(f.n.adapter.executable, wrapper, 0700); err != nil {
		t.Fatal(err)
	}
	f.config.Mode = ""
	f.write(t)
	clock = clock.Add(5 * time.Second)
	if f.drain(t, 1).Submitted != 1 || f.prompts(t) != 1 {
		t.Fatal("known start failure could not recover")
	}
}
func TestNotificationsClosureAndRebindDuringSubmission(t *testing.T) {
	for _, change := range []string{"close", "rebind"} {
		t.Run(change, func(t *testing.T) {
			f := noticeTestFixture(t)
			f.config.Mode = "prompt_wait"
			f.write(t)
			f.send(t, f.a.Token, "Captured @03-worker")
			result := make(chan error, 1)
			go func() { _, err := f.n.drain(ctx, "", 1); result <- err }()
			f.wait(t, "prompt-started")
			f.send(t, f.a.Token, "Arrival @03-worker")
			switch change {
			case "close":
				_, err := f.service.Store.ChangeSession(ctx, f.operator.Token, "close", SessionChangeRequest{Context: f.ref(0), OperationID: opaqueID(), ExpectedRevision: 2})
				if err != nil {
					t.Fatal(err)
				}
			case "rebind":
				binding := f.config.Binding
				binding.InstanceID = "replacement-instance"
				binding.NativeSessionID = "replacement-native"
				_, err := f.service.Store.BindAgent(ctx, f.operator.Token, "rebind", AgentBindingRequest{Context: f.ref(0), OperationID: opaqueID(), AgentID: f.bID, ExpectedRevision: 2, Runtime: &binding, CredentialHash: tokenHash(opaqueID())})
				if err != nil {
					t.Fatal(err)
				}
				f.config.Binding = binding
			}
			f.release(t)
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			if countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='submitted'") != 1 || countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='pending'") != 1 {
				t.Fatal("inflight outcome consumed new arrivals")
			}
			if change == "close" {
				if f.drain(t, 4).Checked != 0 || f.prompts(t) != 1 {
					t.Fatal("closed Session dispatched new work")
				}
				_, err := f.service.Store.ChangeSession(ctx, f.operator.Token, "reopen", SessionChangeRequest{Context: f.ref(0), OperationID: opaqueID(), ExpectedRevision: 3})
				if err != nil {
					t.Fatal(err)
				}
			}
			f.config.Mode = ""
			f.write(t)
			if f.drain(t, 4).Submitted != 1 || f.prompts(t) != 2 {
				t.Fatal("explicit resumption/new binding did not drain remaining fresh work")
			}
		})
	}
}

func TestNotificationsProductionHarnessSupportPredicate(t *testing.T) {
	for _, test := range []struct {
		kind, version string
		want          bool
	}{
		// The verified minor line from the fixture version up, nothing else.
		{"codex", "0.164.0", true}, {"codex", "0.164.1", true}, {"codex", "0.164.27", true}, {"codex", "0.165.0", false}, {"codex", "1.164.0", false},
		{"codex", "0.163.9", false}, {"codex", "0.164.0-dev", false}, {"codex", "0.164", false}, {"codex", "0.164.01", false}, {"codex", "v0.164.0", false},
		{"codex", "0.164.0.1", false}, {"codex", "", false}, {"pi", "0.164.0", false}, {"claude", "0.164.0", false}, {"Codex", "0.164.0", false}, {"unknown", "0.164.0", false},
	} {
		t.Run(test.kind+"/"+test.version, func(t *testing.T) {
			binding := RuntimeBinding{Harness: test.kind, HarnessVersion: test.version}
			if verifiedHarness(binding) != test.want || newHerdr("unused").verified(binding) != test.want {
				t.Fatal("production support differs from the verified minor line")
			}
		})
	}
	f := noticeTestFixture(t)
	f.n.adapter.verified = verifiedHarness
	if reason := f.n.adapter.verify(ctx, f.config.Binding); reason != "" {
		t.Fatal("verified production combination rejected", reason)
	}
	for _, pair := range [][2]string{{"codex", "0.165.0"}, {"pi", "0.164.0"}, {"unknown", "0.164.0"}, {"codex", ""}} {
		binding := f.config.Binding
		binding.Harness, binding.HarnessVersion = pair[0], pair[1]
		before := len(f.calls(t))
		if f.n.adapter.verify(ctx, binding) == "" || len(f.calls(t)) != before {
			t.Fatal("unsupported production combination reached external commands")
		}
	}
	for _, binding := range []RuntimeBinding{{Harness: "codex", HarnessVersion: "0.164.0"}, {Harness: "codex", HarnessVersion: "0.164.1"}, {Harness: "unknown", HarnessVersion: "0.164.0"}} {
		if f.n.adapter.verify(ctx, binding) == "" {
			t.Fatal("support predicate bypassed complete identity")
		}
	}
}

// An incomplete or unsupported binding waits for a new binding or a service
// start, not for a timer: idle retries were committed writes every 30 s.
func TestNotificationsParkedTargetWaitsForBinding(t *testing.T) {
	for _, mode := range []string{"incomplete", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			f := noticeTestFixture(t)
			clock := time.Now()
			f.n.clock = func() time.Time { return clock }
			if mode == "incomplete" {
				execTest(t, f.service.Store, "UPDATE agents SET binding='{}' WHERE id=?", f.bID)
			} else {
				f.n.adapter.verified = func(RuntimeBinding) bool { return false }
			}
			f.send(t, f.a.Token, "Parked @03-worker")
			if r := f.drain(t, 4); r.Pending != 1 || countTest(t, f.service.Store, "SELECT count(*) FROM notice_targets WHERE next_attempt=-1") != 1 {
				t.Fatal("unfixable binding not parked", r)
			}
			changes := countTest(t, f.service.Store, "SELECT total_changes()")
			clock = clock.Add(24 * time.Hour)
			if r := f.drain(t, 4); r.Checked != 0 || len(f.calls(t)) != 0 || countTest(t, f.service.Store, "SELECT total_changes()") != changes {
				t.Fatal("parked target retried or written on a timer", r)
			}
			status, err := f.service.notificationStatus(ctx, f.b.Token, NoticeRequest{Context: f.ref(0)})
			if err != nil || status.Targets[0].NextAttempt != -1 {
				t.Fatal("status hides the park", err)
			}
			if mode == "unsupported" {
				// A service start, maybe with a newer support table, tries it again.
				f.n.adapter.verified = func(RuntimeBinding) bool { return true }
				if err = f.service.enableNotifications(newHerdr(filepath.Join(f.dir, "not-installed"))); err != nil {
					t.Fatal(err)
				}
				f.service.notifier.stop()
			} else {
				binding := f.config.Binding
				binding.InstanceID = "rebound-instance"
				if _, err = f.service.Store.BindAgent(ctx, f.operator.Token, "rebind", AgentBindingRequest{Context: f.ref(0), OperationID: opaqueID(), AgentID: f.bID, ExpectedRevision: 2, Runtime: &binding, CredentialHash: tokenHash(opaqueID())}); err != nil {
					t.Fatal(err)
				}
			}
			if countTest(t, f.service.Store, "SELECT count(*) FROM notice_targets WHERE next_attempt=-1") != 0 {
				t.Fatal("park kept after a new binding or a start")
			}
			if r := f.drain(t, 4); r.Submitted != 1 || f.prompts(t) != 1 {
				t.Fatal("parked target not retried", r)
			}
		})
	}
}
