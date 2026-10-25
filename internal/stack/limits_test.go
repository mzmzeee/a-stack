package stack

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

var noKeepAlive = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

func TestWriteBudgetRefillWithInjectedClock(t *testing.T) {
	if WriteBurstBytes < MaxRequestBytes {
		t.Fatal("the burst must hold one request of the maximum size")
	}
	now := time.Unix(1_000_000, 0)
	b := writeBudget{now: func() time.Time { return now }}
	limited := func(agent string, cost int, wait int64) {
		t.Helper()
		err := b.check(agent, cost)
		var e *Error
		if !errors.As(err, &e) || e.Code != "rate_limited" || !e.Retryable || e.RetryAfter != wait || statusCode(e) != http.StatusTooManyRequests {
			t.Fatalf("check(%d) = %v, want rate_limited after %d s", cost, err, wait)
		}
	}
	allowed := func(agent string, cost int) {
		t.Helper()
		if err := b.check(agent, cost); err != nil {
			t.Fatalf("check(%d) = %v", cost, err)
		}
	}
	allowed("a", WriteBurstBytes)
	limited("a", WriteBurstBytes+1, 1)
	b.charge("a", WriteBurstBytes-100)
	allowed("a", 100)
	limited("a", 101, 1)
	limited("a", 100+WriteRefillBytesPerMinute, 60)
	allowed("b", WriteBurstBytes) // another Agent has its own budget

	now = now.Add(30 * time.Second) // half a minute refills half the rate
	allowed("a", 100+WriteRefillBytesPerMinute/2)
	limited("a", 101+WriteRefillBytesPerMinute/2, 1)
	b.charge("a", 100+WriteRefillBytesPerMinute/2)
	limited("a", 2*WriteRefillBytesPerMinute, 120)

	now = now.Add(time.Hour) // the budget never grows past the burst
	allowed("a", WriteBurstBytes)
	limited("a", WriteBurstBytes+1, 1)
	now = now.Add(-time.Minute) // a clock that goes back refills nothing
	allowed("a", WriteBurstBytes)
}

func TestWriteBudgetStopsRunawayMailOnly(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	frozen := time.Now()
	s.budget.mu.Lock()
	s.budget.now = func() time.Time { return frozen }
	s.budget.mu.Unlock()
	body := strings.Repeat("x", 65536)
	first := MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: "runaway-first", Body: body}
	receipt, err := s.SendMail(ctx, f.a.Token, first)
	if err != nil {
		t.Fatal(err)
	}
	sent := 1
	for ; sent < 200; sent++ {
		_, err = s.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: opaqueID(), Body: body})
		if err != nil {
			break
		}
	}
	requireCode(t, err, "rate_limited")
	payload, _ := json.Marshal(first)
	if sent != WriteBurstBytes/len(payload) {
		t.Fatal("runaway sends before the limit:", sent)
	}
	// A replay of a committed change still returns its receipt.
	if again, err := s.SendMail(ctx, f.a.Token, first); err != nil || again != receipt {
		t.Fatal("replay was refused or changed", err)
	}
	// Another Agent is not affected, also over HTTP.
	f.send(t, f.b.Token, body)
	raw, _ := json.Marshal(MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: opaqueID(), Body: body})
	post := func(c Credential) (*http.Response, []byte) {
		req, _ := http.NewRequest("POST", f.service.Discovery.URL+"/v1/mail/send", bytes.NewReader(raw))
		req.Header.Set("X-A-Stack-Instance", f.service.Discovery.InstanceID)
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Content-Type", "application/json")
		response, err := noKeepAlive.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		return response, data
	}
	response, data := post(f.a)
	var envelope struct{ Error Error }
	if response.StatusCode != http.StatusTooManyRequests || response.Header.Get("Retry-After") == "" || json.Unmarshal(data, &envelope) != nil || envelope.Error.Code != "rate_limited" || !envelope.Error.Retryable || envelope.Error.RetryAfter < 1 {
		t.Fatal("rate_limited over HTTP:", response.StatusCode, string(data))
	}
	if response, data = post(f.b); response.StatusCode != http.StatusOK {
		t.Fatal("other Agent refused:", string(data))
	}
	// Time refills the budget.
	s.budget.mu.Lock()
	frozen = frozen.Add(5 * time.Second)
	s.budget.mu.Unlock()
	f.send(t, f.a.Token, body)
}

func TestOperatorHasNoWriteBudget(t *testing.T) {
	service, _, operator := startTest(t)
	big, _ := json.Marshal(strings.Repeat("x", MaxRequestBytes-64))
	for i := 0; i < 6; i++ {
		_, err := service.Store.Mutate(ctx, operator.Token, Operation{ID: opaqueID(), Name: "test.big", Scope: "test", Payload: big}, replayAccess,
			func(context.Context, *sql.Tx, Principal) (Receipt, error) {
				return Receipt{ResourceID: "object", Revision: 1, Outcome: "created"}, nil
			})
		if err != nil {
			t.Fatal(i, err)
		}
	}
}

func setFreeBytes(s *Store, free uint64, err error, calls *int) {
	s.disk.mu.Lock()
	defer s.disk.mu.Unlock()
	s.disk.free = func(string) (uint64, error) { *calls++; return free, err }
	s.disk.at = time.Time{}
}

func TestLowDiskWarningInStorageStatusAndView(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	if free, err := diskFree(s.path); err != nil || free == 0 {
		t.Fatal("statfs:", free, err)
	}
	calls := 0
	setFreeBytes(s, 2<<30, nil, &calls)
	storage := func() StorageStatus {
		t.Helper()
		var envelope struct{ Data StorageStatus }
		status, data := rawRequest(t, f.service, f.operator, "GET", "/v1/storage", nil, nil)
		if status != http.StatusOK || json.Unmarshal(data, &envelope) != nil {
			t.Fatal("storage status:", status, string(data))
		}
		return envelope.Data
	}
	raw, _ := json.Marshal(SnapshotRequest{Context: f.ref(0)})
	snapshot := func(tag string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("POST", f.service.Discovery.URL+"/v1/session/snapshot", bytes.NewReader(raw))
		req.Header.Set("X-A-Stack-Instance", f.service.Discovery.InstanceID)
		req.Header.Set("Authorization", "Bearer "+f.a.Token)
		req.Header.Set("Content-Type", "application/json")
		if tag != "" {
			req.Header.Set("If-None-Match", tag)
		}
		response, err := noKeepAlive.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		return response
	}
	if st := storage(); st.FreeBytes == nil || *st.FreeBytes != 2<<30 || st.LowDisk || st.Warning != "" {
		t.Fatal("enough space reported as low", st)
	}
	first := snapshot("")
	if first.Header.Get("X-A-Stack-Low-Disk") != "" {
		t.Fatal("view warned with enough space")
	}
	storage()
	if calls != 1 {
		t.Fatal("free space was measured per request:", calls)
	}
	setFreeBytes(s, 100<<20, nil, &calls)
	if st := storage(); st.FreeBytes == nil || *st.FreeBytes != 100<<20 || !st.LowDisk || st.Warning == "" {
		t.Fatal("low space not reported", st)
	}
	// The flag reaches the view also on an unchanged (304) snapshot.
	if r := snapshot(first.Header.Get("ETag")); r.StatusCode != http.StatusNotModified || r.Header.Get("X-A-Stack-Low-Disk") != "1" {
		t.Fatal("view misses the low-disk flag", r.StatusCode)
	}
	setFreeBytes(s, 0, errors.New("statfs failed"), &calls)
	if st := storage(); st.FreeBytes != nil || st.LowDisk || st.DatabaseBytes == 0 {
		t.Fatal("unknown free space", st)
	}
	if snapshot("").Header.Get("X-A-Stack-Low-Disk") != "" {
		t.Fatal("unknown free space warned")
	}
}
