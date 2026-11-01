package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// A service is not trusted to choose a local target or enforce the local hold.
func TestRelayDishonestServerCannotBypassLocalInterval(t *testing.T) {
	f, root, exe := relayCLIFixture(t)
	var claims atomic.Int32
	var expiry atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q stack.RelayRequest
		if json.NewDecoder(r.Body).Decode(&q) != nil {
			t.Error("request decoding")
			return
		}

		w.Header().Set("Content-Type", "application/json")

		if r.URL.Path == "/v1/relay/result" {
			w.Write([]byte(`{"data":{}}`))
			return
		}

		now := time.Now().UnixMilli()

		g := stack.RelayGrant{NoticeID: strings.Repeat("a", 42) + "A", Generation: q.Generation, ServerTime: now, ExpiresAt: now + 60000}
		if r.URL.Path == "/v1/relay/claim" {
			claims.Add(1)
			g.LeaseToken = strings.Repeat("b", 42) + "A"
		}

		// Keep the same expiry for the begin response.
		if r.URL.Path == "/v1/relay/begin" {
			g.ExpiresAt = expiry.Load()
		}

		if r.URL.Path == "/v1/relay/claim" {
			expiry.Store(g.ExpiresAt)
		}

		json.NewEncoder(w).Encode(map[string]any{"data": g})
	}))
	defer server.Close()

	discovery, err := stack.ReadDiscovery(f.state)
	if err != nil {
		t.Fatal(err)
	}

	discovery.URL = server.URL
	if err = stack.ReplacePrivate(filepath.Join(f.state, "service.json"), discovery); err != nil {
		t.Fatal(err)
	}

	for range 3 {
		if err = relayAttempt(context.Background(), exe, f.workerFile); err != nil {
			t.Fatal(err)
		}
	}

	if claims.Load() != 1 {
		t.Fatal("local hold allowed another claim")
	}

	prompts, err := os.ReadFile(filepath.Join(root, "prompts"))
	if err != nil {
		t.Fatal(err)
	}

	if strings.Count(string(prompts), "\n") != 1 {
		t.Fatal("local hold allowed duplicate prompt")
	}
}

func TestRelayTwoMachineCapturesAndClonedEnrollment(t *testing.T) {
	f, rootA, exeA := relayCLIFixture(t)

	pathA, _ := relayEnrollmentFile(f.workerFile)
	var captureA relayEnrollment
	if err := stack.ReadRelayFile(pathA, &captureA); err != nil {
		t.Fatal(err)
	}

	credentialB := filepath.Join(rootA, "worker-b.json")
	agentB := f.receipt(f.orchFile, f.scoped("agent", "register", "--name", "03-worker-b", "--new-credential-file", credentialB, "--operation-id", "relay-worker-b-register")...).ResourceID

	rootB := mailWaitPrivate(t)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	exeB := filepath.Join(rootB, "fake-herdr")
	if err = os.WriteFile(exeB, []byte("#!/bin/sh\nGORACE=atexit_sleep_ms=0 exec "+shellQuote(binary)+" -test.run=^TestRelayFakeHerdr$ -- "+shellQuote(rootB)+" \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	f.receipt(credentialB, f.scoped("herdr", "bind", "--agent", agentB, "--expected-revision", "1", "--operation-id", "relay-enroll-machine-b", "--relay", "--notices", "pane", "--herdr-executable", exeB)...)
	f.taskChange(f.orchFile, "relay-assign-b", 3, "assign", "--agent", agentB)
	f.receipt(f.orchFile, f.scoped("mail", "send", "--task", f.task, "--body", "@03-worker-b second machine", "--operation-id", "relay-mail-machine-b")...)

	if err = relayAttempt(context.Background(), exeB, credentialB); err != nil {
		t.Fatal(err)
	}

	clonePath, _ := relayEnrollmentFile(f.workerFile)
	if err = stack.ReplacePrivate(clonePath, captureA); err != nil {
		t.Fatal(err)
	}

	if relayAttempt(context.Background(), exeB, f.workerFile) == nil {
		t.Fatal("cloned enrollment accepted without its machine marker")
	}

	if _, err = os.Stat(filepath.Join(rootA, "prompts")); !os.IsNotExist(err) {
		t.Fatal("B invoked A target")
	}

	for _, name := range []string{"HOME", "USERPROFILE", "AppData", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(name, rootA)
	}

	if err = relayAttempt(context.Background(), exeA, f.workerFile); err != nil {
		t.Fatal(err)
	}

	for _, root := range []string{rootA, rootB} {
		data, err := os.ReadFile(filepath.Join(root, "prompts"))
		if err != nil || strings.Count(string(data), "\n") != 1 {
			t.Fatal("wrong per-machine prompt count")
		}
	}
}

func TestRelayLocalRefusalsNeverPrompt(t *testing.T) {
	for _, mode := range []string{"hook", "backward-clock", "missing-interval", "blocked", "unknown", "stopped", "identity-changed", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			f, root, exe := relayCLIFixture(t)

			path, _ := relayEnrollmentFile(f.workerFile)
			var e relayEnrollment
			if err := stack.ReadRelayFile(path, &e); err != nil {
				t.Fatal(err)
			}

			dir, _ := relayIntervalDir(e)
			switch mode {
			case "hook":
				e.Binding.Notices = "hook"
				if err := stack.ReplacePrivate(path, e); err != nil {
					t.Fatal(err)
				}
			case "unsupported":
				e.Binding.HarnessVersion = "99.0.0"
				if err := stack.ReplacePrivate(path, e); err != nil {
					t.Fatal(err)
				}
			case "backward-clock":
				if err := stack.ReplacePrivate(filepath.Join(dir, "interval.json"), relayInterval{Version: 1, CheckedAt: time.Now().Add(time.Hour).UnixMilli(), NotBefore: time.Now().Add(2 * time.Hour).UnixMilli()}); err != nil {
					t.Fatal(err)
				}
			case "missing-interval":
				if err := os.Remove(filepath.Join(dir, "interval.json")); err != nil {
					t.Fatal(err)
				}
			default:
				terminal := "terminal-1"
				status := mode
				if mode == "identity-changed" {
					terminal = "replaced"
					status = "idle"
				}

				metadata := map[string]any{"result": map[string]any{"agent": map[string]any{"name": "relay-worker", "pane_id": "w1:p1", "terminal_id": terminal, "agent": "codex", "agent_status": status, "agent_session": map[string]any{"agent": "codex", "kind": "id", "source": "herdr:codex", "value": "native-1"}}}}
				if err := stack.ReplacePrivate(filepath.Join(root, "metadata.json"), metadata); err != nil {
					t.Fatal(err)
				}
			}

			err := relayAttempt(context.Background(), exe, f.workerFile)
			if (mode == "hook" || mode == "backward-clock" || mode == "missing-interval") && err == nil {
				t.Fatal("invalid local record accepted")
			}

			if _, err = os.Stat(filepath.Join(root, "prompts")); !os.IsNotExist(err) {
				t.Fatal("unsafe target prompted")
			}
		})
	}
}

func TestRelayGrantRejectsUntrustedFieldsAndBounds(t *testing.T) {
	base := stack.RelayGrant{NoticeID: strings.Repeat("a", 42) + "A", LeaseToken: strings.Repeat("b", 42) + "A", Generation: 2, ServerTime: 100, ExpiresAt: 60100}
	encode := func(g stack.RelayGrant) []byte { b, _ := json.Marshal(map[string]any{"data": g}); return b }
	if _, err := decodeRelayGrant(encode(base), true); err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{"endpoint", "pane_id", "executable", "prompt", "native_session_id", "runtime_id"} {
		t.Run(field, func(t *testing.T) {
			data := strings.TrimSuffix(string(encode(base)), "}}") + `,"` + field + `":"untrusted"}}`
			if _, err := decodeRelayGrant([]byte(data), true); err == nil {
				t.Fatal("server routing accepted")
			}
		})
	}

	for _, expiry := range []int64{0, 100, 60101, 1<<63 - 1} {
		g := base
		g.ExpiresAt = expiry
		if _, err := decodeRelayGrant(encode(g), true); err == nil {
			t.Fatal("invalid TTL accepted")
		}
	}

	if _, err := decodeRelayGrant(append(encode(base), make([]byte, 8192)...), true); err == nil {
		t.Fatal("oversize accepted")
	}

	if _, err := decodeRelayGrant(encode(base), false); err == nil {
		t.Fatal("begin lease token accepted")
	}
}
