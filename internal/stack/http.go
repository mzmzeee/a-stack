package stack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const MaxRequestBytes = 1 << 20

func decodeJSON(data []byte, target any) error {
	if len(data) > MaxRequestBytes || !utf8.Valid(data) {
		return failure("invalid_input", "request must be bounded UTF-8 JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return failure("invalid_input", "malformed JSON or unknown fields")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return failure("invalid_input", "exactly one JSON value required")
	}
	return nil
}
func requestJSON(w http.ResponseWriter, r *http.Request, target any) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return failure("invalid_input", "Content-Type application/json required")
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return failure("invalid_input", "request exceeds limit or could not be read")
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return failure("invalid_input", "JSON object required")
	}
	return decodeJSON(data, target)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, err error) {
	e := publicError(err)
	if e.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(e.RetryAfter, 10))
	}
	writeJSON(w, statusCode(e), map[string]any{"error": e})
}

type Health struct {
	DatabaseID    string `json:"database_id"`
	SQLiteVersion string `json:"sqlite_version"`
	SchemaVersion int    `json:"schema_version"`
	Version       string `json:"version"`
}
type Service struct {
	Store     *Store
	Discovery Discovery
	Version   string // running build, for health and the browser view; set before Serve
	Notice    string // one line for stderr when the last port was taken, else empty
	server    *http.Server
	listener  net.Listener
	lock      *stateLock
	once      sync.Once
	closeErr  error
	notifier  *notifier
}

// lastPortFile remembers the port of the last start without --listen, so that
// an open browser tab finds the service again after a restart.
const lastPortFile = "last-port.json"

type lastPort struct {
	Port int `json:"port"`
}

// listenDefault tries the remembered port first and falls back to a free one.
// The notice is set only when a remembered port was taken.
func listenDefault(dir string) (listener net.Listener, notice string, err error) {
	var last lastPort
	if data, err := privateRead(filepath.Join(dir, lastPortFile)); err != nil || decodeJSON(data, &last) != nil || last.Port <= 0 || last.Port >= 65536 {
		last.Port = 0
	} else if listener, err = net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(last.Port))); err == nil {
		return listener, "", nil
	}
	listener, err = net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	// Best effort: a missing record only means a new port next time.
	_ = writePrivate(filepath.Join(dir, lastPortFile), lastPort{port}, true)
	if last.Port != 0 {
		notice = fmt.Sprintf("port %d from the last run is in use; using port %d. Open browser tabs need a-stack again.", last.Port, port)
	}
	return listener, notice, nil
}

// Start acquires ownership before SQLite opens; clients never call this. An
// empty address reuses the last default port when it is free.
// ServiceGCPercent is the garbage collector target of a long-running service.
// Its live heap is about 2 MB, so the default of 100 collected about 280 times a
// second under heavy load. In the heavy-use harness (scripts/bench.py), 400 cut
// service CPU per call by 17-38% for about 10 MB more memory.
const ServiceGCPercent = 400

// TuneServiceGC sets ServiceGCPercent unless GOGC is set in the environment.
// Only the service process calls it; short CLI commands keep the default.
func TuneServiceGC() {
	if _, set := os.LookupEnv("GOGC"); !set {
		debug.SetGCPercent(ServiceGCPercent)
	}
}

func Start(ctx context.Context, dir, address string) (*Service, error) {
	if !builtWithCgo {
		return nil, failure("storage_unavailable", "this a-stack was built without cgo, so it cannot open its SQLite database; build it with CGO_ENABLED=1 and a C compiler, or install a release binary")
	}
	storageLog.setOutput(storageLogOutput)
	if address != "" {
		host, _, err := net.SplitHostPort(address)
		if err != nil || host != "127.0.0.1" {
			return nil, failure("invalid_input", "listen address must be 127.0.0.1:PORT")
		}
	}
	lock, err := lockState(dir)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			lock.close()
		}
	}()
	path, err := ensureDatabaseFile(lock.dir)
	if err != nil {
		return nil, err
	}
	store, err := openStore(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !success {
			store.db.Close()
		}
	}()
	if err = store.ensureOperator(ctx, lock.dir); err != nil {
		return nil, err
	}
	var listener net.Listener
	var notice string
	if address == "" {
		listener, notice, err = listenDefault(lock.dir)
	} else {
		listener, err = net.Listen("tcp4", address)
	}
	if err != nil {
		return nil, failure("conflict", "cannot bind requested local service address")
	}
	defer func() {
		if !success {
			listener.Close()
		}
	}()
	s := &Service{Store: store, Notice: notice, listener: listener, lock: lock, Discovery: Discovery{URL: localURL(listener.Addr().String()), DatabaseID: store.id, InstanceID: opaqueID()}}
	// The stdlib server's error log can include attacker-provided bytes for bad
	// requests. Discard it; startup/serve failures use bounded structured errors.
	s.server = &http.Server{Handler: http.HandlerFunc(s.dispatch), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(io.Discard, "", 0)}
	if err = writePrivate(filepath.Join(lock.dir, "service.json"), s.Discovery, true); err != nil {
		return nil, err
	}
	success = true
	return s, nil
}
func (s *Service) Serve() error {
	err := s.server.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	if err != nil {
		return failure("storage_unavailable", "local HTTP service stopped unexpectedly")
	}
	return nil
}
func (s *Service) Close(ctx context.Context) error {
	s.once.Do(func() {
		if err := s.server.Shutdown(ctx); err != nil {
			_ = s.server.Close()
			s.closeErr = failure("storage_unavailable", "service shutdown exceeded deadline")
		}
		_ = s.listener.Close()
		if s.notifier != nil {
			s.notifier.stop()
			s.notifier.mu.Lock()
			//lint:ignore SA2001 barrier: wait for an HTTP-initiated drain to finish before the database closes.
			s.notifier.mu.Unlock()
		}
		if err := s.Store.db.Close(); err != nil {
			s.closeErr = storageError(err)
		}
		if d, err := ReadDiscovery(s.lock.dir); err == nil && d.InstanceID == s.Discovery.InstanceID {
			_ = os.Remove(filepath.Join(s.lock.dir, "service.json"))
			_ = syncDir(s.lock.dir)
		}
		s.lock.close()
	})
	return s.closeErr
}
func (s *Service) dispatch(w http.ResponseWriter, r *http.Request) {
	if r.Host != strings.TrimPrefix(s.Discovery.URL, "http://") {
		writeError(w, failure("forbidden", "invalid Host"))
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.Discovery.URL {
		writeError(w, failure("forbidden", "cross-origin requests are forbidden"))
		return
	}
	if r.URL.RawQuery != "" || r.URL.User != nil {
		writeError(w, failure("invalid_input", "query parameters are not supported"))
		return
	}
	if s.browserAsset(w, r) {
		return
	}
	if r.Header.Get("X-A-Stack-Instance") != s.Discovery.InstanceID {
		writeError(w, failure("conflict", "service instance changed; reload discovery"))
		return
	}
	if r.URL.Path == "/v1/browser/redeem" {
		s.redeemHTTP(w, r)
		return
	}
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		writeError(w, failure("unauthorized", "valid bearer credential required"))
		return
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	p, err := authenticate(r.Context(), s.Store.db, token)
	if err != nil {
		writeError(w, err)
		return
	}
	if r.ContentLength > MaxRequestBytes {
		writeError(w, failure("invalid_input", "request exceeds limit"))
		return
	}
	if r.Method == http.MethodGet && (r.ContentLength != 0 || len(r.TransferEncoding) != 0) {
		writeError(w, failure("invalid_input", "GET requests must not contain a body"))
		return
	}
	// Session-scoped browser tokens have no foundation data endpoints. They may use
	// the Session read endpoints, read Task mail and answer a Needs you question;
	// default dispatch remains deny. The answer is their one write, so it also
	// needs the Origin of the service, which a browser always sends with a POST.
	answer := r.URL.Path == "/v1/task/answer"
	if p.Kind == BrowserCapability && !taskReadEndpoint(r.URL.Path) && r.URL.Path != "/v1/mail/read" && !answer {
		writeError(w, failure("forbidden", "browser credential requires a Session read endpoint"))
		return
	}
	if p.Kind == BrowserCapability && answer && r.Header.Get("Origin") == "" {
		writeError(w, failure("forbidden", "the view's answer requires the Origin header"))
		return
	}
	switch r.URL.Path {
	case "/v1/task/answer":
		s.answerHTTP(w, r, token)
	case "/v1/health":
		if r.Method != http.MethodGet {
			writeError(w, failure("invalid_input", "GET required"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": Health{s.Store.id, s.Store.sqliteVersion, schemaVersion, s.Version}})
	case "/v1/storage":
		if r.Method != http.MethodGet {
			writeError(w, failure("invalid_input", "GET required"))
			return
		}
		if p.Kind != OperatorCapability {
			writeError(w, failure("forbidden", "Operator credential required"))
			return
		}
		status, err := s.Store.Status()
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": status})
	case "/v1/storage/backup":
		if r.Method != http.MethodPost {
			writeError(w, failure("invalid_input", "POST required"))
			return
		}
		if p.Kind != OperatorCapability {
			writeError(w, failure("forbidden", "Operator credential required"))
			return
		}
		var request struct {
			Destination string `json:"destination"`
		}
		if err = requestJSON(w, r, &request); err != nil {
			writeError(w, err)
			return
		}
		status, err := s.Store.Backup(r.Context(), token, request.Destination)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": status})
	default:
		if !s.notificationHTTP(w, r, token) && !s.herdrHTTP(w, r, token) && !s.browserHTTP(w, r, token) && !s.mailHTTP(w, r, token) && !s.taskHTTP(w, r, token) && !s.lifecycleHTTP(w, r, token) {
			writeError(w, failure("not_found", "endpoint not found"))
		}
	}
}

type Client struct {
	Discovery  Discovery
	Credential Credential
	HTTP       *http.Client
}

func NewClient(dir, credentialFile string, kind CapabilityKind) (*Client, error) {
	if kind != "" && !validKind(kind) {
		return nil, failure("invalid_input", "credential kind must be operator, agent or browser")
	}
	if credentialFile == "" {
		if kind == AgentCapability || kind == BrowserCapability {
			return nil, failure("invalid_input", "agent/browser mode requires an explicit credential file")
		}
		credentialFile = filepath.Join(dir, "operator.json")
	}
	c, err := ReadCredential(credentialFile)
	if err != nil {
		return nil, err
	}
	if kind != "" && kind != c.Kind {
		return nil, failure("forbidden", "credential file has a different capability kind")
	}
	d, err := ReadDiscovery(dir)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(d.URL)
	if err != nil || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Scheme != "http" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, failure("invalid_input", "invalid loopback service URL")
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, DisableKeepAlives: true}
	return &Client{Discovery: d, Credential: c, HTTP: &http.Client{Timeout: 30 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Do returns the complete JSON envelope; no stdout or automatic retry. Mutation
// callers retain their operation ID and expected revision when retrying.
func (c *Client) Do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, failure("invalid_input", "invalid request body")
		}
	}
	if len(data) > MaxRequestBytes {
		return nil, failure("invalid_input", "request exceeds limit")
	}
	if !strings.HasPrefix(path, "/v1/") || strings.ContainsAny(path, "?#\r\n") {
		return nil, failure("invalid_input", "invalid API path")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Discovery.URL+path, bytes.NewReader(data))
	if err != nil {
		return nil, failure("invalid_input", "invalid request")
	}
	req.Header.Set("Authorization", "Bearer "+c.Credential.Token)
	req.Header.Set("X-A-Stack-Instance", c.Discovery.InstanceID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.HTTP.Do(req)
	if err != nil {
		return nil, unreachable(serviceUnreachable)
	}
	defer response.Body.Close()
	b, err := io.ReadAll(io.LimitReader(response.Body, MaxRequestBytes+1))
	if err != nil {
		return nil, unreachable("the response from the a-stack service was cut off; run the same command again")
	}
	if len(b) > MaxRequestBytes {
		return nil, failure("storage_unavailable", "invalid or oversized service response")
	}
	var envelope struct {
		Data  json.RawMessage `json:"data,omitempty"`
		Error *Error          `json:"error,omitempty"`
	}
	if decodeJSON(b, &envelope) != nil || (envelope.Error == nil) == (len(envelope.Data) == 0) {
		return nil, failure("storage_unavailable", "invalid service response")
	}
	if envelope.Error != nil {
		return nil, envelope.Error
	}
	if response.StatusCode != http.StatusOK {
		return nil, failure("storage_unavailable", "unexpected service response")
	}
	return b, nil
}
