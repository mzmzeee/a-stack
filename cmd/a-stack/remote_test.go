package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestConnectCLIAndRemoteFlags(t *testing.T) {
	config := t.TempDir()
	t.Setenv("HOME", config)
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("AppData", config)

	var out, errout bytes.Buffer
	if code := run([]string{"connect", "list"}, &out, &errout); code != 0 || strings.TrimSpace(out.String()) != `{"data":[]}` {
		t.Fatalf("connect list: %d %s %s", code, &out, &errout)
	}

	for _, args := range [][]string{
		{"connect"},
		{"connect", "remove"},
		{"connect", "team", "http://example.com"},
		{"connect", "../escape", "https://example.com"},
		{"connect", "list", "--credential-file", "secret"},
		{"serve", "--remote", "team"},
		{"new", "--remote", "team"},
		{"health", "--share"},
		{"service", "status", "--share"},
		{"health", "--remote", "team"},
		{"health", "--remote", ""},
	} {
		out.Reset()
		errout.Reset()

		if code := run(args, &out, &errout); code == 0 || out.Len() != 0 || !json.Valid(errout.Bytes()) {
			t.Fatalf("accepted %v: %d %s %s", args, code, &out, &errout)
		}
	}

	if err := execute([]string{"hook", "--remote", "team"}, &out, &errout); err == nil {
		t.Fatal("hook accepted remote")
	}

	for _, cmd := range []string{"health", "task list", "mail check", "watch", "mcp", "open"} {
		if !strings.Contains(acceptedFlags(cmd), " remote ") {
			t.Errorf("%s misses remote", cmd)
		}
	}
}

func TestServiceInstallShareStartupAndExistingService(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		f, _, state, _ := serviceSetup(t)
		f.replies["enable --now a-stack"] = func() (string, error) {
			enabled, err := stack.ShareEnabled(state)
			if err != nil || !enabled {
				t.Fatalf("sharing not stored before service start: %v %v", enabled, err)
			}

			service, err := stack.Start(context.Background(), state, "")
			if err != nil {
				t.Fatal(err)
			}

			if err = service.SetServeHost("server.example.ts.net"); err != nil {
				t.Fatal(err)
			}

			done := make(chan error, 1)
			go func() { done <- service.Serve() }()
			t.Cleanup(func() { service.Close(context.Background()); <-done })

			return "", nil
		}

		out, _ := runService(t, 0, "install", "--share", "--state-dir", state, "--json")
		if !strings.Contains(out, `"shared_url":"https://server.example.ts.net"`) {
			t.Fatalf("missing shared URL: %s", out)
		}
	})
	t.Run("running", func(t *testing.T) {
		f, _, state, _ := serviceSetup(t)
		startTestService(t, state)
		f.replies["is-active --quiet a-stack"] = func() (string, error) { return "", nil }

		out, _ := runService(t, 0, "install", "--share", "--state-dir", state, "--json")
		if !strings.Contains(out, `"restart_hint"`) {
			t.Fatalf("missing restart hint: %s", out)
		}

		for _, call := range f.calls {
			if strings.Contains(call, "restart") {
				t.Fatalf("restarted service: %s", call)
			}
		}

		runService(t, 0, "install", "--state-dir", state)

		enabled, err := stack.ShareEnabled(state)
		if err != nil || enabled {
			t.Fatalf("sharing not disabled: %v %v", enabled, err)
		}
	})
	t.Run("foreground-conflict", func(t *testing.T) {
		_, _, state, _ := serviceSetup(t)
		startTestService(t, state)
		runService(t, 1, "install", "--share", "--state-dir", state)

		if _, err := os.Stat(filepath.Join(state, "share.json")); !os.IsNotExist(err) {
			t.Fatalf("changed settings despite failed preflight: %v", err)
		}
	})
}

func TestShareStartupRejectsNonFixedPortBeforeStarting(t *testing.T) {
	err := serveForeground(context.Background(), filepath.Join(t.TempDir(), "state"), "127.0.0.1:0", "", nil, true)
	if err == nil || !strings.Contains(err.Error(), stack.SharedListen) {
		t.Fatalf("nonfixed port: %v", err)
	}
}

func TestServiceSharingSettingAndReadyOutput(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	// With no ownership record, disabling never needs the Tailscale executable.
	t.Setenv("PATH", t.TempDir())

	for _, enabled := range []bool{true, false} {
		if err := configureServiceShare(state, map[string]any{"share": enabled}); err != nil {
			t.Fatal(err)
		}

		actual, err := stack.ShareEnabled(state)
		if err != nil || actual != enabled {
			t.Fatalf("share %v %v", actual, err)
		}
	}

	if info, err := os.Stat(filepath.Join(state, "share.json")); err != nil || info.Size() > 4096 {
		t.Fatalf("settings file %v %v", info, err)
	}

	service := &stack.Service{}
	if err := service.SetServeHost("server.example.ts.net"); err != nil {
		t.Fatal(err)
	}

	data := readyLine(service)["data"].(map[string]any)
	if data["shared_url"] != "https://server.example.ts.net" {
		t.Fatalf("ready URL %v", data)
	}
}
