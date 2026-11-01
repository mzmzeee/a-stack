package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestRelaySchedulerFairnessAndRetryBounds(t *testing.T) {
	now := time.Now()
	entries := make([]relayEntry, 64)

	cursor := 0
	seen := map[int]bool{}
	for range 16 {
		selected := relayReady(entries, &cursor, nil, now)
		if len(selected) != 4 {
			t.Fatal("tick bound/fairness")
		}

		for _, i := range selected {
			if seen[i] {
				t.Fatal("repeated before full round")
			}

			seen[i] = true
			entries[i].finishAttempt(nil, now)
		}

		now = now.Add(6 * time.Second)
	}

	if len(seen) != 64 {
		t.Fatal("starvation")
	}

	for i := range entries {
		entries[i].server = "one"
	}

	if len(relayReady(entries, &cursor, map[string]time.Time{"one": now.Add(time.Hour)}, now)) != 0 {
		t.Fatal("server cooldown ignored")
	}

	e := relayEntry{}
	for _, seconds := range []int{5, 10, 20, 40, 60, 60} {
		e.finishAttempt(&stack.Error{Code: "storage_unavailable", Retryable: true}, now)

		delay := e.next.Sub(now)
		minimum := time.Duration(seconds) * time.Second
		if e.parked || delay < minimum || delay > minimum+minimum/5 {
			t.Fatal("incorrect disconnected backoff")
		}
	}

	e.finishAttempt(&stack.Error{Code: "rate_limited", RetryAfter: 3600}, now)

	if e.next.Before(now.Add(time.Hour)) {
		t.Fatal("long retry shortened")
	}

	for _, retry := range []int64{-1, 1<<63 - 1} {
		e = relayEntry{}
		e.finishAttempt(&stack.Error{Code: "rate_limited", RetryAfter: retry}, now)

		if !e.parked {
			t.Fatal("overflow retry not parked")
		}
	}

	e = relayEntry{}
	e.finishAttempt(&stack.Error{Code: "unauthorized"}, now)

	if !e.parked || e.waitServer {
		t.Fatal("unauthorized must park only its entry")
	}
}

func TestRelayConfigEntryBound(t *testing.T) {
	root := mailWaitPrivate(t)

	file := filepath.Join(root, "relay.json")
	config := relayConfig{HerdrExecutable: filepath.Join(root, "herdr")}
	for i := range 65 {
		config.Credentials = append(config.Credentials, filepath.Join(root, fmt.Sprintf("agent-%d.json", i)))
	}

	if err := stack.ReplacePrivate(file, config); err != nil {
		t.Fatal(err)
	}

	if runRelay(context.Background(), file, true, io.Discard) == nil {
		t.Fatal("65 entries accepted")
	}

	config.Credentials = config.Credentials[:64]
	if err := stack.ReplacePrivate(file, config); err != nil {
		t.Fatal(err)
	}

	if err := runRelay(context.Background(), file, true, io.Discard); err != nil {
		t.Fatal(err)
	}
}
