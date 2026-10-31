package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharingSafetyPreflight(t *testing.T) {
	linkTestHome(t)

	missing := filepath.Join(t.TempDir(), "missing-state")
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"confirmation", []string{"session", "recover-transfer", "--transfer-id", shareToken()}, "--destination-stopped"},
		{"false confirmation", []string{"session", "recover-transfer", "--transfer-id", shareToken(), "--destination-stopped=false"}, "--destination-stopped"},
		{"missing transfer", []string{"session", "recover-transfer", "--destination-stopped"}, "transfer ID required"},
		{"malformed transfer", []string{"session", "recover-transfer", "--destination-stopped", "--transfer-id", "bogus123"}, "malformed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, remote := range []bool{false, true} {
				args := append(append([]string{}, tc.args...), "--state-dir", missing)
				if remote {
					args = append(args, "--remote", "not-configured")
				}

				var out, stderr bytes.Buffer
				if code := run(args, &out, &stderr); code != 2 || !strings.Contains(stderr.String(), tc.want) || out.Len() != 0 {
					t.Errorf("preflight: code=%d error=%s", code, stderr.String())
				}
			}
		})
	}

	for _, flags := range [][]string{{"--as", "agent"}, {"--as", "browser"}, {"--as", "operator"}, {"--credential-file", filepath.Join(missing, "agent.json")}, {"--as", "operator", "--credential-file", filepath.Join(missing, "agent.json")}} {
		var out, stderr bytes.Buffer
		args := append([]string{"connect-code", "--state-dir", missing}, flags...)
		if code := run(args, &out, &stderr); code != 2 || !strings.Contains(stderr.String(), "does not apply to a-stack connect-code") || out.Len() != 0 {
			t.Errorf("credential flags: code=%d error=%s", code, stderr.String())
		}
	}
}

func TestSharingMissingFileDiagnostics(t *testing.T) {
	linkTestHome(t)
	f := newTeam(t)
	t.Chdir(f.project)

	missing := filepath.Join(t.TempDir(), "missing-state")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"connect-code", "--state-dir", missing}, filepath.Join(missing, "operator.json")},
		{[]string{"session", "invite", "friend@example.com", "--remote", "nope", "--workspace", f.workspace, "--session", f.session, "--state-dir", missing}, "nope.json"},
		{[]string{"session", "link", "team", f.session, "--code", "unused", "--state-dir", f.state}, "team.json"},
	} {
		var out, stderr bytes.Buffer
		if run(tc.args, &out, &stderr) == 0 || !strings.Contains(stderr.String(), tc.want) {
			t.Errorf("missing %q diagnostic: %s", tc.want, stderr.String())
		}
	}
}

func TestInviteHelpOptionalContext(t *testing.T) {
	for _, name := range []string{"workspace", "session", "operation-id"} {
		if flagRequired("session invite", name) {
			t.Errorf("invite --%s incorrectly required", name)
		}
	}

	var out, stderr bytes.Buffer
	if run([]string{"help", "connect-code"}, &out, &stderr) != 0 {
		t.Fatal(stderr.String())
	}

	for _, name := range []string{"--as", "--credential-file", "--remote"} {
		if strings.Contains(out.String(), name) {
			t.Errorf("connect-code advertises ignored flag %s", name)
		}
	}
}
