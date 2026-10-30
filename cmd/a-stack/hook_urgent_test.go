package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestMailReplyUrgentCLI(t *testing.T) {
	f := newTeam(t)
	original := f.receipt(f.workerFile, f.scoped("mail", "send", "--task", f.task, "--body", "parent", "--operation-id", "urgent-cli-parent")...)
	args := f.scoped("mail", "reply", "--message", original.ResourceID, "--body", "stop", "--urgent", "--operation-id", "urgent-cli-reply")
	f.receipt(f.workerFile, args...)
	out, stderr, code := f.run(f.workerFile, f.scoped("mail", "read", "--task", f.task)...)
	var page struct {
		Data stack.MailPage `json:"data"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &page) != nil || len(page.Data.Messages) != 2 || page.Data.Messages[1].Priority != "urgent" {
		t.Fatal(out, stderr)
	}
	if _, stderr, code = f.run(f.workerFile, append(args, "--urgent=false")...); code == 0 {
		t.Fatal("duplicate urgent accepted", stderr)
	}
}

func TestUrgentHooksBypassOnlyNormalThrottle(t *testing.T) {
	for _, harness := range []string{"claude", "codex", "pi", "opencode"} {
		t.Run(harness, func(t *testing.T) {
			f := newTeam(t)
			home := hookHome(t)
			peerFile := filepath.Join(filepath.Dir(f.workerFile), "peer.json")
			peer := f.receipt(f.orchFile, f.scoped("agent", "register", "--name", "03-peer", "--new-credential-file", peerFile, "--operation-id", "urgent-peer")...).ResourceID
			f.taskChange(f.orchFile, "urgent-assign", 2, "assign", "--agent", peer)
			id := hookIdentity{harness: harness, state: f.state, credential: f.workerFile, agent: f.worker, worktree: f.project, ref: stack.Context{WorkspaceID: f.workspace, SessionID: f.session}, noStopGuard: true}
			enrollTestHook(t, id, "abc", "")
			own := filepath.Join(home, "own.json")
			f.run(f.workerFile, f.scoped("mail", "check", "--cursor-file", own)...)
			before, _ := os.ReadFile(own)
			hookEvent(t, id, "SessionStart", "")
			file, _ := hookCacheFile(f.worker, "1/"+harness+"/abc", ".json")
			normalBefore, _ := os.ReadFile(file)
			f.receipt(peerFile, f.scoped("mail", "send", "--task", f.task, "--body", "Normal body", "--operation-id", "urgent-normal")...)
			if out := hookEvent(t, id, "PostToolUse", ""); out != "" {
				t.Fatal("normal bypassed throttle", out)
			}
			for i := range 20 {
				f.receipt(peerFile, f.scoped("mail", "send", "--task", f.task, "--body", "Secret stop instructions", "--urgent", "--operation-id", fmt.Sprintf("urgent-burst-%d", i))...)
			}
			if out := hookEvent(t, id, "PostToolUse", `,"session_id":"unrelated"`); out != "" {
				t.Fatal("unenrolled session received urgency", out)
			}
			off := id
			off.noMailNudge = true
			if out := hookEvent(t, off, "PostToolUse", ""); out != "" {
				t.Fatal("disabled hooks received urgency", out)
			}
			want := `"additionalContext":"` + urgentMailNudge + `"`
			out := hookEvent(t, id, "PostToolUse", "")
			if !strings.Contains(out, want) || strings.Contains(out, "Build the parser") || strings.Contains(out, "Secret") || len(out) > 256 {
				t.Fatal("missing or unsafe urgent nudge", out)
			}
			if out := hookEvent(t, id, "PostToolUse", ""); out != "" {
				t.Fatal("batch replayed", out)
			}
			if now, _ := os.ReadFile(file); string(now) != string(normalBefore) {
				t.Fatal("urgent moved normal cursor")
			}
			if now, _ := os.ReadFile(own); string(now) != string(before) {
				t.Fatal("urgent moved own cursor")
			}
			old := time.Now().Add(-mailNudgeEvery - time.Second)
			if err := os.Chtimes(file, old, old); err != nil {
				t.Fatal(err)
			}
			if out := hookEvent(t, id, "PostToolUse", ""); !strings.Contains(out, "New Task mail") {
				t.Fatal("normal mail lost", out)
			}
			os.Chtimes(file, old, old)
			if out := hookEvent(t, id, "PostToolUse", ""); out != "" {
				t.Fatal("normal scan replayed urgent", out)
			}
			beforeDue, _ := os.ReadFile(file)
			f.receipt(peerFile, f.scoped("mail", "send", "--task", f.task, "--body", "normal due", "--operation-id", "urgent-normal-due")...)
			f.receipt(peerFile, f.scoped("mail", "send", "--task", f.task, "--body", "urgent due", "--urgent", "--operation-id", "urgent-both-due")...)
			os.Chtimes(file, old, old)
			if out := hookEvent(t, id, "PostToolUse", ""); !strings.Contains(out, want) || strings.Contains(out, "Build the parser") {
				t.Fatal("mixed due batch was not generic", out)
			}
			if afterDue, _ := os.ReadFile(file); string(afterDue) == string(beforeDue) {
				t.Fatal("urgent traffic starved due normal scan")
			}
		})
	}
}

type urgentTimeoutTransport struct{ base http.RoundTripper }

func (tr urgentTimeoutTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/v1/mail/urgent" {
		<-r.Context().Done()
		return nil, r.Context().Err()
	}
	return tr.base.RoundTrip(r)
}

func TestUrgentFirstCatchupAndConcurrentHooks(t *testing.T) {
	f := newTeam(t)
	hookHome(t)
	// The Orchestrator must be assigned before it can send mail.
	f.taskChange(f.orchFile, "urgent-orch-assign", 2, "assign", "--agent", f.orch)
	id := hookIdentity{state: f.state, credential: f.workerFile, agent: f.worker, worktree: f.project, ref: stack.Context{WorkspaceID: f.workspace, SessionID: f.session}}
	client, err := stack.NewClient(f.state, f.workerFile, stack.AgentCapability)
	if err != nil {
		t.Fatal(err)
	}
	in := hookInput{SessionID: "urgent-first", Event: "PostToolUse"}
	f.receipt(f.orchFile, f.scoped("mail", "send", "--task", f.task, "--body", "first urgent", "--urgent", "--operation-id", "urgent-first-send")...)
	var wg sync.WaitGroup
	results := make(chan string, 12)
	for range 12 {
		wg.Go(func() { results <- hookMailNudge(context.Background(), client, id, in) })
	}
	wg.Wait()
	close(results)
	count := 0
	for out := range results {
		if out != "" {
			if out != urgentMailNudge {
				t.Fatal(out)
			}
			count++
		}
	}
	if count != 1 {
		t.Fatal("parallel hooks lost or duplicated urgent", count)
	}
	normalFile, _ := hookCacheFile(f.worker, in.SessionID, ".json")
	_ = os.Remove(normalFile)
	for i := range 101 {
		f.receipt(f.orchFile, f.scoped("mail", "send", "--task", f.task, "--body", "normal backlog", "--operation-id", fmt.Sprintf("urgent-backlog-%d", i))...)
	}
	previousPages := catchUpPages
	catchUpPages = 1
	defer func() { catchUpPages = previousPages }()
	// An urgent message arriving while a bounded normal first catch-up is still
	// incomplete must not be discarded by that catch-up.
	if out := hookMailNudge(context.Background(), client, id, in); out != "" {
		t.Fatal(out)
	}
	firstFile, _ := hookCacheFile(f.worker, in.SessionID, ".first.json")
	if _, err := os.Stat(firstFile); err != nil {
		t.Fatal("first catch-up already complete", err)
	}
	f.receipt(f.orchFile, f.scoped("mail", "send", "--task", f.task, "--body", "second urgent", "--urgent", "--operation-id", "urgent-second-send")...)
	base := client.HTTP.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.HTTP.Transport = nudgeFailShowTransport{base: base}
	if out := hookMailNudge(context.Background(), client, id, in); out != urgentMailNudge {
		t.Fatal("urgent needed a title lookup", out)
	}
	f.receipt(f.orchFile, f.scoped("mail", "send", "--task", f.task, "--body", "after timeout", "--urgent", "--operation-id", "urgent-timeout-send")...)
	client.HTTP.Transport = urgentTimeoutTransport{base: base}
	deadline, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if out := hookMailNudge(deadline, client, id, in); out != "" || time.Since(start) > time.Second {
		t.Fatal("timeout did not fail open", out)
	}
	client.HTTP.Transport = base
	if out := hookMailNudge(context.Background(), client, id, in); out != urgentMailNudge {
		t.Fatal("timeout consumed urgent mark", out)
	}
}
