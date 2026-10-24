package stack

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestBackupRejectsUnsafeDestinationsWithoutPublishingFiles(t *testing.T) {
	s, state, operator := startTest(t)
	private := privateTestDir(t)
	inside := filepath.Join(state, "nested")
	if err := os.Mkdir(inside, 0700); err != nil {
		t.Fatal(err)
	}
	// Resolve an ancestor symlink, not merely a symlink as the direct parent.
	alias := filepath.Join(private, "state-alias")
	if err := os.Symlink(state, alias); err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(private, "public")
	if err := os.Mkdir(public, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(public, 0755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(private, "existing.db")
	if err := os.WriteFile(existing, []byte("preserve this destination"), 0600); err != nil {
		t.Fatal(err)
	}
	agent := issueTest(t, s.Store, AgentCapability, opaqueID(), opaqueID())
	for _, tc := range []struct {
		name, token, destination, code string
	}{
		{"relative", operator.Token, "relative.db", "invalid_input"},
		{"live database", operator.Token, filepath.Join(state, "state.db"), "invalid_input"},
		{"state directory", operator.Token, filepath.Join(state, "backup.db"), "invalid_input"},
		{"nested state", operator.Token, filepath.Join(inside, "backup.db"), "invalid_input"},
		{"symlinked state ancestor", operator.Token, filepath.Join(alias, "nested", "backup.db"), "invalid_input"},
		{"public directory", operator.Token, filepath.Join(public, "backup.db"), "invalid_input"},
		{"missing directory", operator.Token, filepath.Join(private, "missing", "backup.db"), "invalid_input"},
		{"existing destination", operator.Token, existing, "conflict"},
		{"Agent", agent.Token, filepath.Join(private, "agent.db"), "forbidden"},
		{"unknown token", opaqueID(), filepath.Join(private, "unknown.db"), "unauthorized"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Store.Backup(ctx, tc.token, tc.destination)
			requireCode(t, err, tc.code)
			if tc.destination != existing && tc.destination != filepath.Join(state, "state.db") {
				if _, err = os.Lstat(tc.destination); !os.IsNotExist(err) {
					t.Fatal("rejected backup published a destination", err)
				}
			}
		})
	}
	data, err := os.ReadFile(existing)
	if err != nil || string(data) != "preserve this destination" {
		t.Fatal("rejected backup overwrote an existing file", err)
	}
	staging, err := filepath.Glob(filepath.Join(private, ".a-stack-backup-*"))
	if err != nil || len(staging) != 0 {
		t.Fatal("rejected backup left staging files", err)
	}
	if _, err = s.Store.Backup(ctx, operator.Token, filepath.Join(private, "valid.db")); err != nil {
		t.Fatal("backup guards left the service unable to back up", err)
	}
}

func TestServiceShutdownDeadlineReleasesStateAndOldCloseCannotRemoveNewDiscovery(t *testing.T) {
	dir := privateTestDir(t)
	s, err := Start(ctx, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	// Hold one HTTP request until forced shutdown cancels its context. This
	// reaches the deadline path without timing assumptions or a slow handler.
	entered, exited := make(chan struct{}), make(chan struct{})
	s.server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(exited)
	})
	served := make(chan error, 1)
	go func() { served <- s.Serve() }()
	t.Cleanup(func() { _ = s.Close(ctx) })
	requestCtx, cancelRequest := context.WithTimeout(ctx, 5*time.Second)
	defer cancelRequest()
	req, err := http.NewRequestWithContext(requestCtx, "GET", s.Discovery.URL+"/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	requested := make(chan struct{})
	go func() {
		defer close(requested)
		response, err := http.DefaultClient.Do(req)
		if err == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-requestCtx.Done():
		t.Fatal("request did not reach the controlled shutdown boundary")
	}
	lockBefore, err := os.Stat(filepath.Join(dir, "service.lock"))
	if err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithCancel(ctx)
	cancel()
	closeErr := s.Close(deadline)
	requireCode(t, closeErr, "storage_unavailable")
	for _, done := range []<-chan struct{}{exited, requested} {
		select {
		case <-done:
		case <-requestCtx.Done():
			t.Fatal("forced shutdown left an HTTP request running")
		}
	}
	if err = <-served; err != nil {
		t.Fatal("normal HTTP server close reported a serve failure", err)
	}
	if _, err = os.Stat(filepath.Join(dir, "service.json")); !os.IsNotExist(err) {
		t.Fatal("shutdown deadline left stale discovery", err)
	}
	restarted, err := Start(ctx, dir, "")
	if err != nil {
		t.Fatal("shutdown deadline leaked the state lock or database", err)
	}
	defer restarted.Close(ctx)
	if restarted.Discovery.DatabaseID != s.Discovery.DatabaseID || restarted.Discovery.InstanceID == s.Discovery.InstanceID {
		t.Fatal("restart did not retain database identity with a fresh service instance")
	}
	if err = s.Close(ctx); err != closeErr {
		t.Fatal("repeated Close did not retain its original result")
	}
	discovery, err := ReadDiscovery(dir)
	if err != nil || !reflect.DeepEqual(discovery, restarted.Discovery) {
		t.Fatal("old Close removed or replaced the restarted service's discovery", err)
	}
	lockAfter, err := os.Stat(filepath.Join(dir, "service.lock"))
	if err != nil || !os.SameFile(lockBefore, lockAfter) {
		t.Fatal("shutdown replaced the persistent lock file", err)
	}
}
