package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestMailWaitMatches(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message stack.MailMessage
		want    bool
	}{
		{"chatter", stack.MailMessage{}, false},
		{"other mention", stack.MailMessage{MentionIDs: []string{"other"}}, false},
		{"mention", stack.MailMessage{MentionIDs: []string{"me"}}, true},
		{"urgent", stack.MailMessage{Priority: "urgent"}, true},
		{"Operator", stack.MailMessage{SenderKind: "operator"}, true},
		{"ack", stack.MailMessage{Acks: []stack.MailAck{{RecipientID: "me"}}}, true},
		{"other ack", stack.MailMessage{Acks: []stack.MailAck{{RecipientID: "other"}}}, false},
		{"withdrawn ack", stack.MailMessage{Acks: []stack.MailAck{{RecipientID: "me", WithdrawnAt: "then"}}}, false},
		{"done ack", stack.MailMessage{Acks: []stack.MailAck{{RecipientID: "me", AckedAt: "then"}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mailWaitMatches(tc.message, "me", false); got != tc.want {
				t.Fatalf("match=%v want %v", got, tc.want)
			}

			if !mailWaitMatches(tc.message, "me", true) {
				t.Fatal("--all missed other mail")
			}

			tc.message.SenderID = "me"
			for _, all := range []bool{false, true} {
				if mailWaitMatches(tc.message, "me", all) {
					t.Fatal("own mail woke wait")
				}
			}
		})
	}
}

func mailWaitPrivate(t *testing.T) string {
	t.Helper()

	root := filepath.Join(t.TempDir(), "private")
	if err := stack.MkdirPrivate(root); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"HOME", "USERPROFILE", "AppData", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(name, root)
	}

	return root
}

func TestMailWaitCLI(t *testing.T) {
	root := mailWaitPrivate(t)
	f := newTeam(t)
	f.taskChange(f.orchFile, "wait-assign-orch", 2, "assign", "--agent", f.orch)

	file := filepath.Join(root, "mail.json")
	for _, flags := range [][]string{{}, {"--cursor-file", "-"}, {"--cursor-file", file, "--timeout", "0s"}, {"--cursor-file", file, "--timeout", "-1s"}, {"--cursor-file", file, "--timeout", "wrong"}} {
		_, errout, code := f.run(f.workerFile, f.scoped(append([]string{"mail", "wait"}, flags...)...)...)
		if code != 2 {
			t.Fatalf("bad flags %v: %d %s", flags, code, errout)
		}
	}

	out, errout, code := f.run(f.workerFile, f.scoped("mail", "check", "--cursor-file", file)...)
	if code != 0 {
		t.Fatal(out, errout)
	}

	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	f.receipt(f.orchFile, f.scoped("mail", "send", "--task", f.task, "--body", "Question @02-worker", "--operation-id", "wait-question-1")...)

	for range 2 {
		out, errout, code = f.run(f.workerFile, f.scoped("mail", "wait", "--cursor-file", file)...)
		if code != 0 || !strings.Contains(out, `"count":1`) || !strings.Contains(out, f.task) {
			t.Fatalf("wake: %d %s %s", code, out, errout)
		}

		after, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("wait advanced cursor")
		}
	}

	out, errout, code = f.run(f.workerFile, f.scoped("mail", "check", "--cursor-file", file)...)
	if code != 0 || !strings.Contains(out, "Question @02-worker") {
		t.Fatal("wait consumed mail", out, errout)
	}

	out, errout, code = f.run(f.workerFile, f.scoped("mail", "wait", "--cursor-file", file, "--timeout", "1ms")...)
	if code != 5 || !strings.Contains(errout, "mail_wait_timeout") || out != "" {
		t.Fatalf("timeout: %d %s %s", code, out, errout)
	}

	foreign := filepath.Join(root, "foreign.json")
	if err := os.WriteFile(foreign, []byte(`{"identity":"wrong"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, errout, code = f.run(f.workerFile, f.scoped("mail", "wait", "--cursor-file", foreign)...)
	if code != 2 || !strings.Contains(errout, "another database") {
		t.Fatal("foreign cursor accepted", code, errout)
	}

	f.receipt("", f.scoped("session", "close", "--expected-revision", "2", "--operation-id", "wait-close-session")...)

	_, errout, code = f.run(f.workerFile, f.scoped("mail", "wait", "--cursor-file", file)...)
	if code != 1 || !strings.Contains(errout, "session_closed") {
		t.Fatal("closed wait", code, errout)
	}
}

// Fake reads exercise pagination, backoff and revocation deterministically, without sleeps.
func TestMailWaitPolling(t *testing.T) {
	for _, mode := range []string{"arrival", "pages", "rate", "rate-deadline", "closed", "revoked", "error"} {
		t.Run(mode, func(t *testing.T) {
			file := filepath.Join(mailWaitPrivate(t), "missing.json")
			checks, inspects := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/session/inspect" {
					inspects++
					if mode == "revoked" && inspects == 2 {
						fmt.Fprint(w, `{"error":{"code":"unauthorized","message":"revoked"}}`)
						return
					}

					fmt.Fprintf(w, `{"data":{"your_agent_id":"me","closed":%v}}`, mode == "closed" && inspects == 2)
					return
				}

				if r.URL.Path != "/v1/mail/check" {
					t.Error("unexpected path", r.URL.Path)
					return
				}

				checks++

				var req stack.MailCheckRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}

				if !req.Peek {
					t.Error("wait must not mark mail read or re-arm notices")
				}

				if mode == "rate-deadline" {
					fmt.Fprint(w, `{"error":{"code":"rate_limited","message":"slow down","retry_after_seconds":9223372036854775807}}`)
					return
				}

				if mode == "rate" && checks == 1 {
					fmt.Fprint(w, `{"error":{"code":"rate_limited","message":"slow down","retry_after_seconds":42}}`)
					return
				}

				if mode == "error" {
					fmt.Fprint(w, `{"error":{"code":"forbidden","message":"no access"}}`)
					return
				}

				page := stack.MailCheckPage{Tasks: []stack.MailPage{{TaskID: "task", Cursor: "position"}}}
				if mode == "pages" {
					if checks == 1 {
						page.NextTask = "next"
						page.More = true
						page.Tasks[0].More = true
					} else if checks == 2 {
						if req.AfterTask != "next" || req.Cursors["task"] != "position" {
							t.Error("lost Task continuation")
						}

						page.Tasks = []stack.MailPage{{TaskID: "second", Cursor: "second-position"}}
					} else {
						if req.AfterTask != "" || req.Cursors["second"] != "second-position" {
							t.Error("lost scan round")
						}

						page.Tasks[0].Messages = []stack.MailMessage{{SenderID: "other", Priority: "urgent"}}
					}
				} else if checks >= 6 || mode == "rate" && checks == 2 {
					page.Tasks[0].Messages = []stack.MailMessage{{SenderID: "other", MentionIDs: []string{"me"}}}
				}

				if err := json.NewEncoder(w).Encode(map[string]any{"data": page}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			client := &stack.Client{Discovery: stack.Discovery{URL: server.URL}, Credential: stack.Credential{Kind: stack.AgentCapability}, HTTP: server.Client()}
			delays := []time.Duration{}

			data, err := mailWait(context.Background(), client, stack.Context{}, file, time.Minute, false, func(_ context.Context, d time.Duration) error {
				delays = append(delays, d)

				if mode == "rate-deadline" {
					return context.DeadlineExceeded
				}

				if len(delays) > 8 {
					t.Fatal("spinning")
				}

				return nil
			})
			wantError := map[string]string{"rate-deadline": "mail_wait_timeout", "closed": "session_closed", "revoked": "unauthorized", "error": "forbidden"}[mode]
			if wantError != "" {
				e, ok := err.(*stack.Error)
				if !ok || e.Code != wantError {
					t.Fatalf("want %s: %v", wantError, err)
				}
			} else if err != nil || !bytes.Contains(data, []byte(`"count":1`)) {
				t.Fatalf("wake %s %v", data, err)
			}

			if mode == "pages" && len(delays) != 0 {
				t.Fatal("slept with retained unread mail", delays)
			}

			if mode == "arrival" && !slices.Equal(delays, []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 30 * time.Second, 30 * time.Second}) {
				t.Fatal("backoff", delays)
			}

			if mode == "rate-deadline" && (len(delays) != 1 || delays[0] <= 30*time.Second || delays[0] > time.Minute) {
				t.Fatal("retry delay exceeded deadline or overflowed", delays)
			}

			if mode == "rate" && !slices.Equal(delays, []time.Duration{42 * time.Second}) {
				t.Fatal("retry-after", delays)
			}

			if _, err := os.Stat(file); !os.IsNotExist(err) {
				t.Fatal("wait created cursor", err)
			}
		})
	}
}

func TestMailWaitRetainedPages(t *testing.T) {
	root := mailWaitPrivate(t)
	f := newTeam(t)
	f.taskChange(f.orchFile, "wait-pages-assign", 2, "assign", "--agent", f.orch)

	for i := range 105 {
		f.receipt(f.orchFile, f.scoped("mail", "send", "--task", f.task, "--body", "chatter", "--operation-id", fmt.Sprintf("wait-pages-%d", i))...)
	}

	f.receipt(f.orchFile, f.scoped("mail", "send", "--task", f.task, "--body", "urgent past first page", "--urgent", "--operation-id", "wait-pages-urgent")...)

	client, err := stack.NewClient(f.state, f.workerFile, stack.AgentCapability)
	if err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(root, "missing.json")

	data, err := mailWait(context.Background(), client, stack.Context{WorkspaceID: f.workspace, SessionID: f.session}, file, time.Minute, false, func(context.Context, time.Duration) error {
		t.Fatal("slept before finding already unread mail")
		return nil
	})
	if err != nil || !bytes.Contains(data, []byte(`"count":1`)) {
		t.Fatalf("retained pages: %s %v", data, err)
	}

	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("created cursor", err)
	}
}

func TestMailWaitDefaults(t *testing.T) {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	var flags mailFlags
	flags.register(fs)

	if flags.timeout != 30*time.Minute {
		t.Fatal("default timeout", flags.timeout)
	}

	text := helpOutput(t, "mail", "wait", "--help")
	for _, want := range []string{"--cursor-file FILE", "read without changing it", "(required)", "--timeout DURATION", "except your own"} {
		if !strings.Contains(text, want) {
			t.Fatal("missing help", want)
		}
	}
}

func TestMailWaitAllCLI(t *testing.T) {
	root := mailWaitPrivate(t)
	f := newTeam(t)
	f.taskChange(f.orchFile, "wait-all-assign", 2, "assign", "--agent", f.orch)
	file := filepath.Join(root, "mail.json")
	f.receipt(f.workerFile, f.scoped("mail", "send", "--task", f.task, "--body", "self @02-worker", "--urgent", "--operation-id", "wait-self-urgent")...)

	client, err := stack.NewClient(f.state, f.workerFile, stack.AgentCapability)
	if err != nil {
		t.Fatal(err)
	}

	ref := stack.Context{WorkspaceID: f.workspace, SessionID: f.session}
	// A cancelled pause ends each empty scan without a scheduler-dependent sleep.
	stop := func(context.Context, time.Duration) error { return context.Canceled }
	if _, err := mailWait(context.Background(), client, ref, file, time.Minute, true, stop); err != errMailWaitTimeout {
		t.Fatal("self mail woke wait", err)
	}

	for i := range 2 {
		f.receipt(f.orchFile, f.scoped("mail", "send", "--task", f.task, "--body", "status chatter", "--operation-id", fmt.Sprintf("wait-chatter-%d", i))...)
	}

	if _, err := mailWait(context.Background(), client, ref, file, time.Minute, false, stop); err != errMailWaitTimeout {
		t.Fatal("chatter woke default", err)
	}

	out, errout, code := f.run(f.workerFile, f.scoped("mail", "wait", "--cursor-file", file, "--all")...)
	var wake struct {
		Data struct {
			Count int `json:"count"`
			Tasks []struct {
				TaskID string `json:"task_id"`
				Count  int    `json:"count"`
			} `json:"tasks"`
		} `json:"data"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &wake) != nil || wake.Data.Count != 2 || len(wake.Data.Tasks) != 1 || wake.Data.Tasks[0].TaskID != f.task || wake.Data.Tasks[0].Count != 2 {
		t.Fatal("--all", code, out, errout)
	}

	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("created cursor", err)
	}

	_, errout, code = f.run("", f.scoped("mail", "wait", "--cursor-file", file)...)
	if code != 1 || !strings.Contains(errout, "forbidden") {
		t.Fatal("Operator wait", code, errout)
	}
}

func TestMailWaitPauseDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := pauseMailWait(ctx, 30*time.Second); err != context.Canceled {
		t.Fatal(err)
	}
}
