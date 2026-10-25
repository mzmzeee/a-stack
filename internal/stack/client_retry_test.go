package stack

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A request that fails after it may have reached the service is retryable:
// the same command with the same operation ID returns the receipt.
func TestClientTransportErrorsAreRetryable(t *testing.T) {
	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close() // the request arrived; the response never does
		}
	}))
	defer cut.Close()
	partial := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte(`{"data":`))
	}))
	defer partial.Close()
	for _, url := range []string{cut.URL, partial.URL} {
		client := &Client{Discovery: Discovery{URL: url, InstanceID: opaqueID()}, Credential: Credential{Kind: AgentCapability, Token: opaqueID()}, HTTP: &http.Client{}}
		_, err := client.Do(ctx, "POST", "/v1/mail/send", map[string]string{"operation_id": "retry-after-cut"})
		e, ok := err.(*Error)
		if !ok || e.Code != "storage_unavailable" || !e.Retryable {
			t.Fatalf("%s: %v", url, err)
		}
	}
}
