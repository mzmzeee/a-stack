package stack

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type transferProbeBody struct {
	reads   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (b *transferProbeBody) Read([]byte) (int, error) {
	b.reads.Add(1)

	if b.entered != nil {
		close(b.entered)
		<-b.release
	}

	return 0, io.EOF
}
func (*transferProbeBody) Close() error { return nil }

func TestTransferRejectsBeforeReadingBody(t *testing.T) {
	f := tasksFixture(t)
	friend := opaqueID()
	execTest(t, f.service.Store, "INSERT INTO capabilities(token_hash,kind,subject_id,session_id) VALUES(?,'operator',?,'')", tokenHash(friend), opaqueID())
	execTest(t, f.service.Store, "INSERT INTO session_operators(token_hash,session_id,login) VALUES(?,?,?)", tokenHash(friend), f.sessions[0], "friend@example.com")

	for _, tc := range []struct{ name, path, token string }{{"agent import", "import", f.a.Token}, {"friend import", "import", friend}, {"agent export", "export", f.a.Token}, {"unknown import", "import", opaqueID()}} {
		t.Run(tc.name, func(t *testing.T) {
			body := &transferProbeBody{}
			req := httptest.NewRequest(http.MethodPost, "/v1/session/transfer/"+tc.path, body)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			f.service.transferHTTP(w, req, tc.token)

			if body.reads.Load() != 0 {
				t.Fatal("unauthorized transfer read its body")
			}

			if w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestTransferInFlightLimit(t *testing.T) {
	f := tasksFixture(t)
	body := &transferProbeBody{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	req := httptest.NewRequest(http.MethodPost, "/v1/session/transfer/import", body)
	req.Header.Set("Content-Type", "application/json")
	go func() { defer close(done); f.service.transferHTTP(httptest.NewRecorder(), req, f.operator.Token) }()
	var once sync.Once
	release := func() { once.Do(func() { close(body.release); <-done }) }
	defer release()

	select {
	case <-body.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first transfer never reached body")
	}

	for _, path := range []string{"import", "export"} {
		probe := &transferProbeBody{}
		request := httptest.NewRequest(http.MethodPost, "/v1/session/transfer/"+path, probe)
		request.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		f.service.transferHTTP(w, request, f.operator.Token)

		var result struct {
			Error Error `json:"error"`
		}
		if json.Unmarshal(w.Body.Bytes(), &result) != nil || !result.Error.Retryable || result.Error.Code != "rate_limited" || probe.reads.Load() != 0 {
			t.Errorf("concurrent %s was not refused before read: %s", path, w.Body.String())
		}
	}

	release()
	probe := &transferProbeBody{}
	request := httptest.NewRequest(http.MethodPost, "/v1/session/transfer/export", probe)
	request.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.service.transferHTTP(w, request, f.operator.Token)

	if probe.reads.Load() != 1 || w.Code != http.StatusBadRequest {
		t.Fatal("failed request did not release transfer gate", w.Code)
	}
}

func TestTransferExportRequestLimit(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/session/transfer/export", strings.NewReader(`{"repository":"`+strings.Repeat("x", MaxRequestBytes)+`"}`))
	request.Header.Set("Content-Type", "application/json")

	var target TransferExportRequest
	if transferJSON(httptest.NewRecorder(), request, &target) == nil {
		t.Fatal("export decoded more than the normal request limit")
	}
}
