package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestRelayKillAfterPromptBeforeResult(t *testing.T) {
	f, root, exe := relayCLIFixture(t)
	discovery, err := stack.ReadDiscovery(f.state)
	if err != nil {
		t.Fatal(err)
	}

	reached := make(chan struct{}, 1)
	release := make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/relay/result" {
			reached <- struct{}{}
			<-release
			return
		}

		req, e := http.NewRequestWithContext(r.Context(), r.Method, discovery.URL+r.URL.Path, r.Body)
		if e != nil {
			t.Error(e)
			return
		}

		req.Header = r.Header.Clone()

		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	defer proxy.Close()

	altered := discovery
	altered.URL = proxy.URL
	if err = stack.ReplacePrivate(filepath.Join(f.state, "service.json"), altered); err != nil {
		t.Fatal(err)
	}

	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	childCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	child := exec.CommandContext(childCtx, binary, "-test.run=^TestRelayProcessHelper$", "--", exe, f.workerFile)
	if err = child.Start(); err != nil {
		close(release)
		t.Fatal(err)
	}

	select {
	case <-reached:
	case <-childCtx.Done():
		close(release)
		child.Wait()
		t.Fatal("accepted-result barrier not reached")
	}

	before, err := os.ReadFile(filepath.Join(root, "prompts"))
	if err != nil {
		child.Process.Kill()
		close(release)
		child.Wait()
		t.Fatal(err)
	}

	if strings.Count(string(before), "\n") != 1 {
		child.Process.Kill()
		close(release)
		child.Wait()
		t.Fatal("expected one accepted prompt")
	}

	child.Process.Kill()
	child.Wait()
	close(release)

	if err = stack.ReplacePrivate(filepath.Join(f.state, "service.json"), discovery); err != nil {
		t.Fatal(err)
	}

	f.receipt(f.orchFile, f.scoped("mail", "send", "--task", f.task, "--body", "@02-worker later", "--operation-id", "reviewer-after-kill-mail")...)

	if err = relayAttempt(context.Background(), exe, f.workerFile); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(filepath.Join(root, "prompts"))
	if err != nil || string(after) != string(before) {
		t.Fatal("restart bypassed hold or replayed prompt", err)
	}

	c, err := stack.NewClient(f.state, f.workerFile, stack.AgentCapability)
	if err != nil {
		t.Fatal(err)
	}

	data, err := c.Do(context.Background(), "POST", "/v1/notifications/status", stack.NoticeRequest{Context: stack.Context{WorkspaceID: f.workspace, SessionID: f.session}})
	if err != nil {
		t.Fatal(err)
	}

	var result struct{ Data stack.NoticeStatus }
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}

	if len(result.Data.Targets) != 1 || result.Data.Targets[0].Counts[stack.NoticeSubmitting] != 1 || result.Data.Targets[0].Counts[stack.NoticePending] != 1 {
		t.Fatal("captured/later refs not distinct after kill")
	}
}
