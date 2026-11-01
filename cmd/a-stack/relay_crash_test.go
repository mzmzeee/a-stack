package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestRelayProcessHelper(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--" {
			if relayAttempt(context.Background(), os.Args[i+1], os.Args[i+2]) != nil {
				os.Exit(3)
			}

			os.Exit(0)
		}
	}
}

func TestRelayKilledAfterBeginCannotExecuteRecoveredGrant(t *testing.T) {
	f, root, exe := relayCLIFixture(t)
	discovery, err := stack.ReadDiscovery(f.state)
	if err != nil {
		t.Fatal(err)
	}

	reached := make(chan struct{}, 1)
	release := make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, err := http.NewRequestWithContext(r.Context(), r.Method, discovery.URL+r.URL.Path, r.Body)
		if err != nil {
			t.Error("proxy request")
			return
		}

		request.Header = r.Header.Clone()

		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return
		}
		defer response.Body.Close()

		data, err := io.ReadAll(response.Body)
		if err != nil {
			return
		}

		if r.URL.Path == "/v1/relay/begin" {
			reached <- struct{}{}
			<-release
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.StatusCode)
		w.Write(data)
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

	processCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	child := exec.CommandContext(processCtx, binary, "-test.run=^TestRelayProcessHelper$", "--", exe, f.workerFile)
	if err = child.Start(); err != nil {
		close(release)
		t.Fatal(err)
	}

	select {
	case <-reached:
	case <-processCtx.Done():
		close(release)
		child.Wait()
		t.Fatal("begin barrier not reached")
	}

	enrollmentFile, _ := relayEnrollmentFile(f.workerFile)
	var enrollment relayEnrollment
	if err = stack.ReadRelayFile(enrollmentFile, &enrollment); err != nil {
		close(release)
		child.Process.Kill()
		child.Wait()
		t.Fatal(err)
	}

	intervalDir, _ := relayIntervalDir(enrollment)
	if unlock, ok := tryLock(intervalDir); ok {
		unlock()
		close(release)
		child.Process.Kill()
		child.Wait()
		t.Fatal("second process acquired active relay lock")
	}

	if err = child.Process.Kill(); err != nil {
		close(release)
		t.Fatal(err)
	}

	child.Wait()
	close(release)

	if err = stack.ReplacePrivate(filepath.Join(f.state, "service.json"), discovery); err != nil {
		t.Fatal(err)
	}

	if err = relayAttempt(context.Background(), exe, f.workerFile); err != nil {
		t.Fatal(err)
	}

	if _, err = os.Stat(filepath.Join(root, "prompts")); !os.IsNotExist(err) {
		t.Fatal("recovered grant prompted")
	}
}
