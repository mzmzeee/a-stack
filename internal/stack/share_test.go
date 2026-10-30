package stack

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const ownedServeJSON = `{"TCP":{"443":{"HTTPS":true}},"Web":{"server.example.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:41677"}}}}}`

func fakeTailscale(t *testing.T) string {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("fake executable uses a POSIX shell")
	}

	bin := privateTestDir(t)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_TS_LOG"
if [ "$FAKE_TS_FAIL" = 1 ]; then exit 1; fi
case "$*" in
 'status --json') printf '%s' "$FAKE_TS_STATUS" ;;
 'serve status --json') /bin/cat "$FAKE_TS_CONFIG" ;;
 'serve --bg --https=443 http://127.0.0.1:41677') printf '%s' "$FAKE_TS_OWNED" > "$FAKE_TS_CONFIG" ;;
 'serve --https=443 off') printf '{}' > "$FAKE_TS_CONFIG" ;;
 *) exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "tailscale"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", bin)
	t.Setenv("FAKE_TS_LOG", filepath.Join(bin, "calls"))
	t.Setenv("FAKE_TS_CONFIG", filepath.Join(bin, "config"))
	t.Setenv("FAKE_TS_OWNED", ownedServeJSON)
	t.Setenv("FAKE_TS_STATUS", `{"BackendState":"Running","Self":{"DNSName":"server.example.ts.net."}}`)

	if err := os.WriteFile(os.Getenv("FAKE_TS_CONFIG"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	return bin
}

func TestShareSetupIdempotenceAndCleanup(t *testing.T) {
	fakeTailscale(t)

	dir := privateTestDir(t)
	s := &Service{lock: &stateLock{dir: dir}, Discovery: Discovery{URL: "http://" + SharedListen}}
	for range 2 {
		url, err := s.ConfigureShare(context.Background(), true)
		if err != nil || url != "https://server.example.ts.net" {
			t.Fatalf("configure %q %v", url, err)
		}
	}

	calls, _ := os.ReadFile(os.Getenv("FAKE_TS_LOG"))
	if strings.Count(string(calls), "serve --bg") != 1 {
		t.Fatalf("setup not idempotent: %s", calls)
	}

	if err := DisableShare(ctx, dir); err != nil {
		t.Fatal(err)
	}

	if err := DisableShare(ctx, dir); err != nil {
		t.Fatal(err)
	}

	calls, _ = os.ReadFile(os.Getenv("FAKE_TS_LOG"))
	if strings.Count(string(calls), "serve --https=443 off") != 1 {
		t.Fatalf("cleanup not idempotent: %s", calls)
	}

	if strings.Contains(string(calls), "reset") || strings.Contains(string(calls), "funnel") {
		t.Fatalf("destructive command: %s", calls)
	}
}

func TestShareRefusesForeignRulesAndChangedOwnership(t *testing.T) {
	for _, config := range []string{
		ownedServeJSON, // same target but no ownership record
		`{"TCP":{"443":{"TCPForward":"localhost:22"}}}`,
		`{"AllowFunnel":{"server.example.ts.net:443":true}}`,
		`{"Foreground":{"session":{}}}`,
	} {
		t.Run(config, func(t *testing.T) {
			fakeTailscale(t)

			if err := os.WriteFile(os.Getenv("FAKE_TS_CONFIG"), []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}

			dir := privateTestDir(t)
			s := &Service{lock: &stateLock{dir: dir}, Discovery: Discovery{URL: "http://" + SharedListen}}
			_, err := s.ConfigureShare(ctx, true)
			requireCode(t, err, "conflict")
			// Disabling without a record never touches a foreign rule.
			if err = DisableShare(ctx, dir); err != nil {
				t.Fatal(err)
			}

			got, _ := os.ReadFile(os.Getenv("FAKE_TS_CONFIG"))
			if string(got) != config {
				t.Fatal("foreign rule changed")
			}
		})
	}

	t.Run("changed-owned", func(t *testing.T) {
		fakeTailscale(t)

		dir := privateTestDir(t)
		s := &Service{lock: &stateLock{dir: dir}, Discovery: Discovery{URL: "http://" + SharedListen}}
		if _, err := s.ConfigureShare(ctx, true); err != nil {
			t.Fatal(err)
		}

		changed := strings.Replace(ownedServeJSON, "41677", "9999", 1)
		if err := os.WriteFile(os.Getenv("FAKE_TS_CONFIG"), []byte(changed), 0o600); err != nil {
			t.Fatal(err)
		}

		err := DisableShare(ctx, dir)
		requireCode(t, err, "conflict")

		got, _ := os.ReadFile(os.Getenv("FAKE_TS_CONFIG"))
		if string(got) != changed {
			t.Fatal("modified another owner's rule")
		}
	})
}

func TestShareErrorsAndSettings(t *testing.T) {
	fakeTailscale(t)
	dir := privateTestDir(t)
	s := &Service{lock: &stateLock{dir: dir}, Discovery: Discovery{URL: "http://" + SharedListen}}
	t.Setenv("FAKE_TS_STATUS", `{"BackendState":"NeedsLogin"}`)

	_, err := s.ConfigureShare(ctx, true)
	if err == nil || !strings.Contains(err.Error(), "logged out") {
		t.Fatalf("logged out: %v", err)
	}

	t.Setenv("FAKE_TS_FAIL", "1")

	_, err = s.ConfigureShare(ctx, true)
	if err == nil || !strings.Contains(err.Error(), "HTTPS/Serve") {
		t.Fatalf("Serve unavailable: %v", err)
	}

	t.Setenv("PATH", privateTestDir(t))

	_, err = s.ConfigureShare(ctx, true)
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing binary: %v", err)
	}

	for _, enabled := range []bool{true, false} {
		if err = SaveShareSetting(dir, enabled); err != nil {
			t.Fatal(err)
		}

		got, err := ShareEnabled(dir)
		if err != nil || got != enabled {
			t.Fatalf("settings %v %v", got, err)
		}
	}
}
