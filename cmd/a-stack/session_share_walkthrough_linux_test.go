//go:build linux

package main

// The child processes trust the fixture CA through Linux's SSL_CERT_FILE;
// separate processes also keep certificate roots and config homes independent.

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestSharingRealCLIChild(t *testing.T) {
	if os.Getenv("SHARING_CLI_CHILD") == "" {
		t.Skip("subprocess only")
	}

	var args []string
	if json.Unmarshal([]byte(os.Getenv("SHARING_CLI_ARGS")), &args) != nil {
		os.Exit(90)
	}

	os.Exit(run(args, os.Stdout, os.Stderr))
}

func TestSharingEnrollmentCodeFlags(t *testing.T) {
	home := hookHome(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))

	for _, args := range [][]string{{"connect", "team", "http://example.invalid", "--code", "dummy"}, {"session", "link", "team", "unused", "--code", "dummy"}} {
		t.Run(args[0], func(t *testing.T) {
			var out, errout bytes.Buffer
			_ = run(args, &out, &errout)
			if strings.Contains(errout.String(), "--code does not apply") {
				t.Fatal(errout.String())
			}
		})
	}
}

func TestSharingDocumentedCLIWorkflow(t *testing.T) {
	ctx := context.Background()
	owner, friend := newTeam(t), newTeam(t)
	root := t.TempDir()
	state := filepath.Join(root, "server")

	server, e := stack.Start(ctx, state, "")
	if e != nil {
		t.Fatal(e)
	}

	server.Version = "test"
	if e = server.SetServeHost("server.example.ts.net"); e != nil {
		t.Fatal(e)
	}

	done := make(chan error, 1)
	go func() { done <- server.Serve() }()
	t.Cleanup(func() { _ = server.Close(ctx); <-done })
	backend, _ := url.Parse(server.Discovery.URL)
	var login atomic.Value
	login.Store("owner@example.com")
	proxy := httptest.NewTLSServer(&httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(backend)
		r.Out.Host = "server.example.ts.net"
		r.Out.Header.Del("Tailscale-User-Login")
		r.Out.Header.Set("Tailscale-User-Login", login.Load().(string))
	}})
	defer proxy.Close()

	cert := filepath.Join(root, "ca.pem")
	if e = os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: proxy.Certificate().Raw}), 0o600); e != nil {
		t.Fatal(e)
	}

	ownerHome, friendHome := filepath.Join(root, "owner-home"), filepath.Join(root, "friend-home")
	for _, h := range []string{ownerHome, friendHome} {
		if e = os.MkdirAll(h, 0o700); e != nil {
			t.Fatal(e)
		}
	}

	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "git@example.com:team/project.git"}} {
		c := exec.Command("git", args...)
		c.Dir = owner.project
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("git: %v %s", e, b)
		}
	}

	manifest := filepath.Join(ownerHome, "agents.json")
	data, _ := json.Marshal(map[string]string{owner.orch: owner.orchFile, owner.worker: owner.workerFile})
	if e = os.WriteFile(manifest, data, 0o600); e != nil {
		t.Fatal(e)
	}

	var lastFailure string
	cli := func(home, cwd, localState string, wantOK bool, args ...string) map[string]json.RawMessage {
		t.Helper()

		if args[0] != "connect" {
			args = append([]string{"--state-dir", localState}, args...)
		}

		encoded, _ := json.Marshal(args)
		c := exec.Command(os.Args[0], "-test.run=^TestSharingRealCLIChild$")
		c.Dir = cwd
		c.Env = append(os.Environ(), "SHARING_CLI_CHILD=1", "SHARING_CLI_ARGS="+string(encoded), "HOME="+home, "PI_CODING_AGENT_DIR="+filepath.Join(home, ".pi", "agent"), "CLAUDE_CONFIG_DIR="+filepath.Join(home, ".claude"), "CODEX_HOME="+filepath.Join(home, ".codex"), "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "XDG_STATE_HOME="+filepath.Join(home, "state"), "XDG_CACHE_HOME="+filepath.Join(home, "cache"), "XDG_DATA_HOME="+filepath.Join(home, "data"), "SSL_CERT_FILE="+cert)
		var out, errout bytes.Buffer
		c.Stdout = &out

		c.Stderr = &errout
		e := c.Run()
		if (e == nil) != wantOK {
			t.Fatalf("CLI %s expected success=%v, error=%v stderr=%s", args[0], wantOK, e, errout.String())
		}

		if !wantOK {
			lastFailure = errout.String()
			return nil
		}

		var env struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if e = json.Unmarshal(out.Bytes(), &env); e != nil {
			t.Fatalf("decode %s output: %v", args[0], e)
		}

		return env.Data
	}
	value := func(m map[string]json.RawMessage, key string) string {
		var s string
		if e := json.Unmarshal(m[key], &s); e != nil {
			t.Fatalf("missing %s", key)
		}

		return s
	}
	ownerCode := value(cli(ownerHome, owner.project, state, true, "connect-code"), "code")
	cli(ownerHome, owner.project, owner.state, true, "connect", "team", proxy.URL, "--code", ownerCode)
	cli(ownerHome, owner.project, owner.state, true, "connect", "team", proxy.URL, "--code", ownerCode)
	shared := cli(ownerHome, owner.project, owner.state, true, "session", "share", "--remote", "team", "--agent-files", manifest)
	session := value(shared, "session_id")
	cli(ownerHome, owner.project, filepath.Join(root, "missing-owner-service"), true, "session", "invite", "other@example.com", "--remote", "team", "--workspace", value(shared, "workspace_id"), "--session", session)
	inviteCode := value(cli(ownerHome, owner.project, owner.state, true, "session", "invite", "friend@example.com"), "code")
	login.Store("friend@example.com")
	cli(friendHome, friend.project, friend.state, true, "connect", "team", proxy.URL)
	cli(friendHome, friend.project, friend.state, false, "session", "link", "--", "team", session)
	cli(friendHome, friend.project, friend.state, false, "session", "link", "--code", "wrong", "--", "team", session)
	login.Store("stranger@example.com")
	cli(friendHome, friend.project, friend.state, false, "session", "link", "--code", inviteCode, "--", "team", session)
	login.Store("friend@example.com")
	linked := cli(friendHome, friend.project, friend.state, true, "session", "link", "--code", inviteCode, "--", "team", session)
	friendCredential := value(linked, "credential_file")
	cli(friendHome, friend.project, filepath.Join(root, "missing-friend-service"), true, "session", "invite", "third@example.com", "--remote", "team", "--credential-file", friendCredential, "--workspace", value(shared, "workspace_id"), "--session", session)
	cli(friendHome, friend.project, friend.state, true, "session", "link", "--code", inviteCode, "--", "team", session)
	cli(friendHome, friend.project, friend.state, true, "session", "inspect", "--remote", "team", "--credential-file", friendCredential)
	// A separate client credential cannot consume the invitation again.
	anotherHome := filepath.Join(root, "another-home")
	if e = os.MkdirAll(anotherHome, 0o700); e != nil {
		t.Fatal(e)
	}

	cli(anotherHome, friend.project, friend.state, true, "connect", "team", proxy.URL)
	cli(anotherHome, friend.project, friend.state, false, "session", "link", "--code", inviteCode, "--", "team", session)
	// A second machine may use a different local profile name for this server.
	cli(friendHome, friend.project, friend.state, true, "connect", "friendteam", proxy.URL)
	friendAgentFile := filepath.Join(friendHome, "friend-agent.json")
	registered := cli(friendHome, friend.project, friend.state, true, "agent", "register", "--remote", "friendteam", "--credential-file", friendCredential, "--workspace", value(shared, "workspace_id"), "--session", session, "--name", "friend-worker", "--new-credential-file", friendAgentFile, "--operation-id", "friend-register-agent")
	friendAgentID := value(registered, "resource_id")

	data, _ = json.Marshal(map[string]string{owner.orch: owner.orchFile, owner.worker: owner.workerFile, friendAgentID: friendAgentFile})
	if e = os.WriteFile(manifest, data, 0o600); e != nil {
		t.Fatal(e)
	}

	login.Store("owner@example.com")
	cli(ownerHome, owner.project, owner.state, false, "session", "unshare", "--agent-files", manifest)

	if !strings.Contains(lastFailure, "a-stack connect friendteam "+proxy.URL) {
		t.Fatal("missing profile did not name its connect command", lastFailure)
	}

	// Both aliases exist locally and point to exactly the same URL/database.
	cli(ownerHome, owner.project, owner.state, true, "connect", "friendteam", proxy.URL)
	cli(ownerHome, owner.project, owner.state, true, "session", "unshare", "--agent-files", manifest)
	cli(ownerHome, owner.project, owner.state, true, "session", "inspect", "--workspace", owner.workspace, "--session", owner.session)
	login.Store("friend@example.com")
	cli(friendHome, friend.project, friend.state, false, "session", "inspect", "--remote", "team", "--credential-file", friendCredential)
	t.Log("PASS: real CLI owner bootstrap/default routing, share, invite, friend connect/link, wrong/missing code and wrong login, retry and single use, mixed-profile friend Agent, missing-profile recovery, unshare")
}
