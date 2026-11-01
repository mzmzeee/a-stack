package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

type relayConfig struct {
	HerdrExecutable string   `json:"herdr_executable"`
	Credentials     []string `json:"credentials"`
}
type relayEntry struct {
	credential string
	next       time.Time
	failures   int
	parked     bool
	generation string
	server     string
	waitServer bool
}

func runRelay(parent context.Context, file string, once bool, out io.Writer) error {
	if file == "" {
		return invalid("relay requires --config FILE")
	}

	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	var previous string
	var entries []relayEntry
	cursor := 0

	serverNext := map[string]time.Time{}
	serverFailures := map[string]int{}
	for {
		var config relayConfig
		if err := stack.ReadRelayFile(file, &config); err != nil {
			return err
		}

		if !filepath.IsAbs(config.HerdrExecutable) || len(config.Credentials) == 0 || len(config.Credentials) > 64 {
			return invalid("relay config requires an absolute herdr_executable and 1 to 64 credential paths")
		}

		encoded, _ := json.Marshal(config)
		if string(encoded) != previous {
			previous = string(encoded)
			entries = nil
			cursor = 0
			serverNext = map[string]time.Time{}
			serverFailures = map[string]int{}

			seen := map[string]bool{}
			for _, path := range config.Credentials {
				if !filepath.IsAbs(path) || seen[path] {
					return invalid("relay credential paths must be absolute and unique")
				}

				seen[path] = true
				entries = append(entries, relayEntry{credential: path})
			}
		}

		var wg sync.WaitGroup

		for i := range entries {
			entry := &entries[i]
			generation := credentialGeneration(entry.credential)
			if generation != entry.generation {
				entry.generation = generation
				entry.parked = false
				entry.failures = 0
				entry.next = time.Time{}
			}

			if entry.server == "" {
				file, fileErr := relayEnrollmentFile(entry.credential)
				var enrollment relayEnrollment
				if fileErr == nil && stack.ReadRelayFile(file, &enrollment) == nil {
					entry.server = enrollment.Database
				}
			}
		}

		selected := relayReady(entries, &cursor, serverNext, time.Now())
		for _, i := range selected {
			entry := &entries[i]
			entry.waitServer = false
			entry.failures = max(entry.failures, serverFailures[entry.server])
			wg.Add(1)
			go func(e *relayEntry) {
				defer wg.Done()
				err := relayAttempt(ctx, config.HerdrExecutable, e.credential)
				e.finishAttempt(err, time.Now())
			}(entry)
		}

		wg.Wait()

		for _, i := range selected {
			e := entries[i]
			if e.failures == 0 && !e.parked {
				serverFailures[e.server] = 0
			}
		}

		for _, i := range selected {
			e := entries[i]
			if e.waitServer && e.server != "" {
				if e.next.After(serverNext[e.server]) {
					serverNext[e.server] = e.next
				}

				serverFailures[e.server] = max(serverFailures[e.server], e.failures)
			}
		}

		if once {
			return json.NewEncoder(out).Encode(map[string]any{"data": map[string]any{"attempted": len(selected)}})
		}

		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (e *relayEntry) finishAttempt(err error, now time.Time) {
	delay := 5 * time.Second
	e.waitServer = false
	if err != nil {
		e.waitServer = true
		e.failures = min(e.failures+1, 5)

		delay = time.Duration(min(5<<uint(e.failures-1), 60)) * time.Second
		var api *stack.Error
		if errors.As(err, &api) {
			if api.Code == "unauthorized" || api.Code == "invalid_input" || api.Code == "forbidden" {
				e.parked = true
				e.waitServer = false
			}

			if api.RetryAfter < 0 || api.RetryAfter > int64(((1<<63-1)-int64(12*time.Second))/int64(time.Second)) {
				e.parked = true
			} else if time.Duration(api.RetryAfter)*time.Second > delay {
				delay = time.Duration(api.RetryAfter) * time.Second
			}
		}
	} else {
		e.failures = 0
	}

	e.next = now.Add(delay + time.Duration(rand.Int64N(int64(min(delay/5, 12*time.Second))+1)))
}

func relayReady(entries []relayEntry, cursor *int, serverNext map[string]time.Time, now time.Time) []int {
	selected := []int{}
	for scanned := 0; scanned < len(entries) && len(selected) < 4; scanned++ {
		i := *cursor
		*cursor = (*cursor + 1) % len(entries)

		e := entries[i]
		if !e.parked && !now.Before(e.next) && !now.Before(serverNext[e.server]) {
			selected = append(selected, i)
		}
	}

	return selected
}

func relayAttempt(ctx context.Context, executable, credential string) error {
	file, err := relayEnrollmentFile(credential)
	if err != nil {
		return err
	}

	var e relayEnrollment
	if err = stack.ReadRelayFile(file, &e); err != nil {
		return err
	}

	machine, err := hookMachine(false)
	if err != nil || relayHash(machine) != e.Machine || e.Machine != e.Binding.RelayMachine || relayHash(e.Proof) != e.Binding.RelayKeyHash || !stack.ValidID(e.Proof) || e.Revision < 1 || e.Credential != credentialGeneration(credential) || e.Binding.Notices != "pane" {
		return invalid("relay enrollment is not current on this machine")
	}

	dir, err := relayIntervalDir(e)
	if err != nil {
		return err
	}

	unlock, ok := tryLock(dir)
	if !ok {
		return nil
	}

	defer unlock()

	interval, err := readRelayInterval(dir)
	if err != nil {
		return err
	}

	started := time.Now()
	if started.UnixMilli() < interval.CheckedAt {
		return invalid("relay clock moved backwards")
	}

	if started.UnixMilli() < interval.NotBefore {
		return nil
	}

	discoveryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	var client *stack.Client
	if e.Remote != "" {
		client, err = stack.NewRemoteClient(discoveryCtx, e.Remote, credential, stack.AgentCapability)
	} else {
		client, err = stack.NewClientContext(discoveryCtx, e.StateDir, credential, stack.AgentCapability)
	}

	cancel()

	if err != nil {
		return err
	}

	if client.Discovery.DatabaseID != e.Database || client.Credential.WorkspaceID != e.Context.WorkspaceID || client.Credential.SessionID != e.Context.SessionID {
		return invalid("relay server or context pin differs")
	}

	client.HTTP.Timeout = 10 * time.Second
	q := stack.RelayRequest{Context: e.Context, Generation: e.Revision, RuntimeID: e.Binding.InstanceID, Proof: e.Proof}
	started = time.Now()

	data, err := client.Do(ctx, "POST", "/v1/relay/claim", q)
	if err != nil {
		return err
	}

	grant, err := decodeRelayGrant(data, true)
	if err != nil {
		return err
	}

	if grant.NoticeID == "" {
		return nil
	}

	if grant.Generation != e.Revision {
		return invalid("relay grant generation differs")
	}

	deadline := started.Add(time.Duration(grant.ExpiresAt-grant.ServerTime) * time.Millisecond)

	q.NoticeID, q.LeaseToken = grant.NoticeID, grant.LeaseToken
	if reason := stack.RelayVerify(ctx, executable, e.Binding); reason != "" {
		q.Outcome, q.Reason = "deferred", reason
		_, err = client.Do(ctx, "POST", "/v1/relay/result", q)
		return err
	}

	if !relayTimeValid(started, deadline) {
		return invalid("relay lease elapsed or clock changed")
	}

	reserved, err := reserveRelayInterval(dir, time.Now())
	if err != nil || !reserved {
		return err
	}

	data, err = client.Do(ctx, "POST", "/v1/relay/begin", q)
	if err != nil {
		return err
	}

	begun, err := decodeRelayGrant(data, false)
	if err != nil || begun.NoticeID != grant.NoticeID || begun.Generation != e.Revision || begun.ExpiresAt != grant.ExpiresAt {
		return invalid("invalid relay begin grant; input suppressed")
	}

	state := stack.NoticeUncertain
	if relayTimeValid(started, deadline) {
		attemptCtx, cancel := context.WithDeadline(ctx, deadline)
		state = stack.RelayPrompt(attemptCtx, executable, e.Binding, deadline)
		cancel()
	}

	q.Outcome = string(state)
	_, err = client.Do(ctx, "POST", "/v1/relay/result", q)

	return err
}

func relayTimeValid(started, deadline time.Time) bool {
	now := time.Now()
	elapsed := now.Sub(started)
	wall := time.Duration(now.UnixMilli()-started.UnixMilli()) * time.Millisecond
	delta := wall - elapsed
	return elapsed >= 0 && wall >= 0 && delta < 2*time.Second && delta > -2*time.Second && now.Before(deadline) && now.UnixMilli() < deadline.UnixMilli()
}

func strictRelayJSON(data []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()

	if d.Decode(value) != nil || d.Decode(new(any)) != io.EOF {
		return invalid("invalid relay response")
	}

	return nil
}

func decodeRelayGrant(data []byte, claim bool) (stack.RelayGrant, error) {
	var reply struct {
		Data stack.RelayGrant `json:"data"`
	}
	if len(data) > 8192 {
		return reply.Data, invalid("relay response exceeds limit")
	}

	// Strict fields reject server-selected routing, rather than ignoring it.
	if err := strictRelayJSON(data, &reply); err != nil {
		return reply.Data, err
	}

	g := reply.Data
	if claim && g == (stack.RelayGrant{}) {
		return g, nil
	}

	if !stack.ValidID(g.NoticeID) || g.Generation < 1 || g.ServerTime < 1 || g.ExpiresAt <= g.ServerTime || g.ExpiresAt-g.ServerTime > 60000 || claim && !stack.ValidID(g.LeaseToken) || !claim && g.LeaseToken != "" {
		return g, invalid("invalid or oversized relay lease")
	}

	return g, nil
}
