package stack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func serveTest(t *testing.T, dir, address string) *Service {
	t.Helper()

	s, err := Start(ctx, dir, address)
	if err != nil {
		t.Fatal(err)
	}

	s.Version = "v9.8.7"
	done := make(chan error, 1)
	go func() { done <- s.Serve() }()
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_ = s.Close(c)
		<-done
	})

	return s
}

func TestRestartReusesPortSoAnOpenBrowserTabRecovers(t *testing.T) {
	f := tasksFixture(t)
	f.create(t, "Survives restart")
	u, token := viewTest(t, f, f.ref(0))
	// The open tab keeps its origin, view token and the old instance ID.
	tab := clientForCredential(t, f, Credential{Kind: BrowserCapability, Token: token})

	old := f.service.Discovery
	var err error
	if err = f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}

	s := serveTest(t, f.dir, "")
	if s.Discovery.URL != old.URL || s.Discovery.InstanceID == old.InstanceID {
		t.Fatalf("restart moved to %s from %s", s.Discovery.URL, old.URL)
	}

	_, err = tab.Do(ctx, "POST", "/v1/session/snapshot", SnapshotRequest{Context: f.ref(0)})
	requireCode(t, err, "conflict")
	// Like app.js: reload the public shell for the new instance, then retry.
	response, err := http.Get(old.URL + u.Path)
	if err != nil {
		t.Fatal(err)
	}

	shell, _ := io.ReadAll(response.Body)
	response.Body.Close()

	if response.Header.Get("X-A-Stack-Instance") != s.Discovery.InstanceID || response.Header.Get("X-A-Stack-Version") != "v9.8.7" || !strings.Contains(string(shell), `<meta name="a-stack-version" content="v9.8.7"`) {
		t.Fatal("shell lacks the new instance or version")
	}

	// The page compares its view files with the service's, to offer a reload after an update.
	assets := response.Header.Get("X-A-Stack-Assets")
	if len(assets) != 24 || !strings.Contains(string(shell), `<meta name="a-stack-assets" content="`+assets+`"`) {
		t.Fatalf("shell lacks the view files version %q", assets)
	}

	tab.Discovery.InstanceID = response.Header.Get("X-A-Stack-Instance")
	if _, err = tab.Do(ctx, "POST", "/v1/session/snapshot", SnapshotRequest{Context: f.ref(0)}); err != nil {
		t.Fatal("browser credential did not survive the restart", err)
	}

	// The credential stays a browser credential: no health, no other Session.
	_, err = tab.Do(ctx, "GET", "/v1/health", nil)
	requireCode(t, err, "forbidden")

	_, err = tab.Do(ctx, "POST", "/v1/session/snapshot", SnapshotRequest{Context: f.ref(1)})
	if err == nil {
		t.Fatal("browser credential widened after restart")
	}

	// An unknown token is still rejected on the reused port.
	tab.Credential.Token = opaqueID()
	_, err = tab.Do(ctx, "POST", "/v1/session/snapshot", SnapshotRequest{Context: f.ref(0)})
	requireCode(t, err, "unauthorized")
}

func TestDefaultStartFallsBackWhenLastPortIsTaken(t *testing.T) {
	dir := privateTestDir(t)
	first := serveTest(t, dir, "")
	port := first.listener.Addr().(*net.TCPAddr).Port
	record := filepath.Join(dir, lastPortFile)

	info, err := os.Lstat(record)
	if err != nil || checkPrivate(info, false) != nil {
		t.Fatal("last port record is missing or not private", err)
	}

	if err = first.Close(ctx); err != nil {
		t.Fatal(err)
	}

	squatter, err := net.Listen("tcp4", first.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer squatter.Close()

	if first.Notice != "" {
		t.Fatal("first start has no last port to report", first.Notice)
	}

	second := serveTest(t, dir, "")
	moved := second.listener.Addr().(*net.TCPAddr).Port
	if moved == port {
		t.Fatal("start reused a taken port")
	}

	if want := fmt.Sprintf("port %d from the last run is in use; using port %d. Open browser tabs need a-stack again.", port, moved); second.Notice != want {
		t.Fatalf("notice = %q, want %q", second.Notice, want)
	}

	health := healthOf(t, dir)
	if health.Version != "v9.8.7" || health.DatabaseID != first.Discovery.DatabaseID {
		t.Fatalf("health = %+v", health)
	}

	if err = second.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// An explicit --listen address never reads or changes the record.
	explicit := serveTest(t, dir, "127.0.0.1:0")
	if explicit.Notice != "" {
		t.Fatal("explicit --listen reported the last port")
	}

	if err = explicit.Close(ctx); err != nil {
		t.Fatal(err)
	}

	var last lastPort
	data, err := privateRead(record)
	if err != nil || json.Unmarshal(data, &last) != nil || last.Port != moved {
		t.Fatalf("record = %s, want port %d", data, moved)
	}

	// A broken record means a free port, not a failed start.
	if err = os.WriteFile(record, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err = serveTest(t, dir, "").Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func healthOf(t *testing.T, dir string) Health {
	t.Helper()

	client, err := NewClient(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}

	data, err := client.Do(ctx, "GET", "/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}

	var h struct {
		Data Health `json:"data"`
	}
	if json.Unmarshal(data, &h) != nil {
		t.Fatal("health JSON")
	}

	return h.Data
}

func TestOwnedStateAndStoppedServiceMessages(t *testing.T) {
	dir := privateTestDir(t)
	s := serveTest(t, dir, "")
	if _, err := Start(ctx, dir, ""); err != ErrStateOwned || ErrStateOwned.Code != "conflict" || !strings.Contains(ErrStateOwned.Message, ServiceCommand("stop")) {
		t.Fatal("second start did not report the owned state directory", err)
	}

	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}

	_, err := NewClient(dir, "", "")
	requireCode(t, err, "storage_unavailable")

	if e, ok := err.(*Error); !ok || !e.Retryable || !strings.Contains(e.Message, "start it with a-stack serve (or "+ServiceCommand("start")+"), or ask the Operator to start it") {
		t.Fatal("stopped service message", err)
	}
}
