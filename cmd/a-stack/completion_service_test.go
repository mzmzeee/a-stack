package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestCompletionServiceIDs(t *testing.T) {
	f := newTeam(t)
	closed := f.receipt(f.orchFile, f.scoped("task", "create", "--title", "Already closed", "--assign", f.orch, "--operation-id", "complete-closed")...).ResourceID
	f.receipt(f.orchFile, f.scoped("task", "status", "--task", closed, "--progress", "cancelled", "--reason", "Not needed", "--expected-revision", "2", "--operation-id", "complete-cancel")...)

	base := []string{"--state-dir", f.state, "--as", "agent", "--credential-file", f.workerFile}
	out := completionOutput(t, append(slices.Clone(base), "task", "show", "--task", "")...)
	if !strings.Contains(out, f.task+"\tBuild the parser\n") || strings.Contains(out, closed) {
		t.Fatalf("open Tasks: %q", out)
	}

	for _, flag := range []string{"agent", "assign"} {
		out := completionOutput(t, append(slices.Clone(base), "task", "create", "--"+flag, "02")...)
		if out != "02-worker\t\n" {
			t.Fatalf("--%s Agent name: %q", flag, out)
		}
	}

	// The service resolves context from legacy files too. This is a private
	// disposable fixture; no credential content enters the test output.
	base[len(base)-1] = legacyCopy(t, f.workerFile)
	if got := completionValues(t, append(slices.Clone(base), "task", "show", "--task", f.task[:8])...); !slices.Contains(got, f.task) {
		t.Fatalf("legacy context completion: %q", got)
	}

	if got := completionOutput(t, "--state-dir", f.state, "--credential-file", f.workerFile, "--session", f.workspace, "task", "show", "--task", ""); got != "" {
		t.Fatal("conflicting credential context emitted candidates")
	}

	unsafe := f.receipt(f.orchFile, f.scoped("task", "create", "--title", "\x1b[31mUnsafe\ntext\tmore", "--operation-id", "complete-unsafe")...).ResourceID
	out = completionOutput(t, append(slices.Clone(base), "task", "show", "--task", "")...)
	if strings.Contains(out, "\x1b") || !strings.Contains(out, unsafe+"\t�[31mUnsafe text more\n") {
		t.Fatal("unsafe title description")
	}

	// Explicit Operator context is respected even when written after the
	// cursor (index 3 is the empty value after --task).
	got := completionValues(t, "--at=3", "task", "show", "--task", "", "--state-dir", f.state, "--workspace", f.workspace, "--session", f.session)
	if !slices.Contains(got, f.task) {
		t.Fatalf("context after cursor: %q", got)
	}
}

func TestCompletionBashService(t *testing.T) {
	bin, err := exec.LookPath("bash")
	if err != nil || runtime.GOOS == "windows" {
		t.Skip("POSIX bash not installed")
	}

	f := newTeam(t)
	path := filepath.Join(t.TempDir(), "completion.bash")
	if err := os.WriteFile(path, []byte(helpOutput(t, "completion", "bash")), 0o600); err != nil {
		t.Fatal(err)
	}

	script := `source "$1"
COMP_WORDS=("$2" task show --task '' --state-dir "$3" --credential-file "$4"); COMP_CWORD=4; _a_stack
[[ ${COMPREPLY[*]} == "$5" ]] || exit 11
COMP_WORDS=("$2" --state-dir "$3" --credential-file "$4" task create --assign 02); COMP_CWORD=8; _a_stack
[[ ${COMPREPLY[*]} == 02-worker ]] || exit 12
`
	if out, err := exec.Command(bin, "--norc", "-c", script, "test", path, completionShim(t), f.state, f.workerFile, f.task).CombinedOutput(); err != nil {
		t.Fatalf("dynamic bash: %v %s", err, out)
	}
}

func TestCompletionStoppedServiceIsSilent(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")

	service, err := stack.Start(context.Background(), state, "")
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- service.Serve() }()

	if err := service.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	<-done
	start := time.Now()

	var stdout, stderr bytes.Buffer
	args := []string{"__complete", "--state-dir", state, "task", "show", "--task", ""}
	if run(args, &stdout, &stderr) != 0 || stdout.Len() != 0 || stderr.Len() != 0 || time.Since(start) > 2*time.Second {
		t.Fatalf("stopped service was not silent and prompt (%s)", time.Since(start))
	}
}

func TestCompletionSlowServiceTimesOut(t *testing.T) {
	f := newTeam(t)
	requested := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested <- struct{}{}
		<-release
	}))
	defer server.Close()
	defer close(release)

	discovery, err := stack.ReadDiscovery(f.state)
	if err != nil {
		t.Fatal(err)
	}

	discovery.URL = server.URL

	data, _ := json.Marshal(discovery)
	if err := os.WriteFile(filepath.Join(f.state, "service.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	out := completionOutput(t, "--state-dir", f.state, "--credential-file", f.workerFile, "task", "show", "--task", "")
	if out != "" || time.Since(start) > 2*time.Second {
		t.Fatalf("timeout output %q after %s", out, time.Since(start))
	}

	select {
	case <-requested:
	default:
		t.Fatal("completion never contacted the service")
	}

	// A bad credential path is silent as well, even with a running service.
	if got := completionOutput(t, "--state-dir", f.state, "--credential-file", filepath.Join(t.TempDir(), "absent"), "task", "show", "--task", ""); got != "" {
		t.Fatal("bad credential printed candidates")
	}
}
