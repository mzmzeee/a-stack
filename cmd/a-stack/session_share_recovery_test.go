package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

type supersedeAfterImport struct {
	base  http.RoundTripper
	after func()
}

func (s *supersedeAfterImport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := s.base.RoundTrip(r)
	if err == nil && r.URL.Path == "/v1/session/transfer/import" && response.StatusCode == http.StatusOK && s.after != nil {
		after := s.after
		s.after = nil
		after()
	}

	return response, err
}

func TestShareSupersededJournalGuidance(t *testing.T) {
	source, dest := newTeam(t), newTeam(t)
	linkTestHome(t)
	t.Chdir(source.project)

	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "git@example.com:team/project.git"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}

	local, err := stack.NewClient(source.state, "", stack.OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}

	destination, err := stack.NewClient(dest.state, "", stack.OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}

	destination.RemoteName = "team"

	base := destination.HTTP.Transport
	if base == nil {
		base = http.DefaultTransport
	}

	destination.HTTP.Transport = &supersedeAfterImport{base: base, after: func() {
		_, err := local.Do(context.Background(), "POST", "/v1/session/transfer/export", stack.TransferExportRequest{Context: stack.Context{WorkspaceID: source.workspace, SessionID: source.session}, TransferID: shareToken(), Repository: "example.com/team/project"})
		if err != nil {
			t.Fatal(err)
		}
	}}
	original := sharingRemoteClient
	sharingRemoteClient = func(context.Context, string, string, stack.CapabilityKind) (*stack.Client, error) {
		return destination, nil
	}
	t.Cleanup(func() { sharingRemoteClient = original })

	manifest := filepath.Join(t.TempDir(), "agents.json")
	data, _ := json.Marshal(map[string]string{source.orch: source.orchFile, source.worker: source.workerFile})
	if err = os.WriteFile(manifest, data, 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{"session", "share", "--state-dir", source.state, "--remote", "team", "--credential-file", filepath.Join(dest.state, "operator.json"), "--agent-files", manifest}
	for range 2 {
		var out, stderr bytes.Buffer
		if run(args, &out, &stderr) == 0 || !strings.Contains(stderr.String(), "recovery journal") || !strings.Contains(stderr.String(), "remove") {
			t.Fatal("superseded journal has no actionable recovery", stderr.String())
		}
	}

	dir, err := transferDirectory()
	if err != nil {
		t.Fatal(err)
	}

	journals, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(journals) != 1 {
		t.Fatal("ambiguous transfer journal was removed", err)
	}
}
