package stack

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Root alone invokes this opt-in test service for a new disposable fixture.
// The test override permits only one complete exact binding, keeping repeated
// queue verification isolated from every other registered target.
func TestNotificationsDedicatedLiveFixtureService(t *testing.T) {
	configPath := os.Getenv("ASTACK_NOTICE_LIVE_FIXTURE")
	if configPath == "" {
		t.Skip("root-owned dedicated fixture only")
	}

	var config struct {
		StateDir   string         `json:"state_dir"`
		Executable string         `json:"executable"`
		ReadyFile  string         `json:"ready_file"`
		StopFile   string         `json:"stop_file"`
		Binding    RuntimeBinding `json:"binding"`
	}

	data, err := privateRead(configPath)
	if err != nil {
		t.Fatal("private fixture configuration unavailable")
	}

	if decodeJSON(data, &config) != nil || !completeHerdrBinding(config.Binding) || config.Binding.Harness != "codex" || config.Binding.HarnessVersion != "0.164.0" || !filepath.IsAbs(config.Executable) || !filepath.IsAbs(config.StateDir) || !filepath.IsAbs(config.ReadyFile) || !filepath.IsAbs(config.StopFile) {
		t.Fatal("invalid isolated fixture configuration")
	}

	service, err := Start(context.Background(), config.StateDir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(context.Background())

	h := newHerdr(config.Executable)
	h.verified = func(b RuntimeBinding) bool { return b == config.Binding }
	if err = service.enableNotifications(h); err != nil {
		t.Fatal(err)
	}

	failures := make(chan error, 1)
	go func() { failures <- service.Serve() }()

	if err = writePrivate(config.ReadyFile, service.Discovery, false); err != nil {
		t.Fatal("fixture readiness unavailable")
	}

	deadline := time.NewTimer(8 * time.Minute)
	defer deadline.Stop()
	check := time.NewTicker(100 * time.Millisecond)
	defer check.Stop()

	for {
		select {
		case err = <-failures:
			t.Fatal("fixture HTTP service stopped", err)
		case <-deadline.C:
			t.Fatal("dedicated fixture deadline reached")
		case <-check.C:
			if _, err = os.Stat(config.StopFile); err == nil {
				return
			}
		}
	}
}
