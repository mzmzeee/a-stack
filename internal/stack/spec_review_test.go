package stack

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
)

func reviewService(t *testing.T, dir string) (*Service, Credential) {
	t.Helper()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := Start(context.Background(), dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	c, err := ReadCredential(filepath.Join(dir, "operator.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}

func reviewCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("wanted %s, got %v", code, err)
	}
}

func reviewCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != want {
		t.Fatalf("%s count=%d, want=%d, error=%v", table, n, want, err)
	}
}

func TestSpecReviewMutationRecovery(t *testing.T) {
	dir := t.TempDir()
	s, c := reviewService(t, dir)
	db := s.Store.db
	if _, err := db.Exec(`CREATE TABLE review_effects (id INTEGER PRIMARY KEY, value BLOB);
CREATE TRIGGER review_fail_receipt BEFORE INSERT ON operation_receipts BEGIN SELECT RAISE(ABORT,'injected receipt failure'); END;`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	op := Operation{ID: "review-operation-001", Name: "review", Scope: "fixture", Payload: json.RawMessage(`{"value":1}`)}
	allow := func(context.Context, *sql.Tx, Principal, Receipt) error { return nil }
	apply := func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		_, err := tx.ExecContext(ctx, "INSERT INTO review_effects(value) VALUES(?)", "fixture")
		return Receipt{ResourceID: "fixture", Revision: 1, Outcome: "created"}, err
	}
	_, err := s.Store.Mutate(ctx, c.Token, op, allow, apply)
	reviewCode(t, err, "storage_unavailable")
	reviewCount(t, db, "review_effects", 0)
	reviewCount(t, db, "operation_receipts", 0)
	if _, err := db.Exec("DROP TRIGGER review_fail_receipt"); err != nil {
		t.Fatal(err)
	}
	first, err := s.Store.Mutate(ctx, c.Token, op, allow, apply)
	if err != nil {
		t.Fatal(err)
	}
	// Discarding the first response must still leave one durable outcome.
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	s, c = reviewService(t, dir)
	db = s.Store.db
	unexpectedApply := func(context.Context, *sql.Tx, Principal) (Receipt, error) {
		t.Error("retry applied a second mutation")
		return Receipt{}, errors.New("unexpected apply")
	}
	for range 2 {
		retry, err := s.Store.Mutate(ctx, c.Token, op, allow, unexpectedApply)
		if err != nil || retry != first {
			t.Fatalf("receipt changed after restart/retry: error=%v", err)
		}
	}
	op.Payload = json.RawMessage(`{"value":2}`)
	_, err = s.Store.Mutate(ctx, c.Token, op, allow, unexpectedApply)
	reviewCode(t, err, "operation_conflict")
	op.Payload = json.RawMessage(`{"value":1}`)
	deny := func(context.Context, *sql.Tx, Principal, Receipt) error {
		return failure("forbidden", "read access removed")
	}
	_, err = s.Store.Mutate(ctx, c.Token, op, deny, unexpectedApply)
	reviewCode(t, err, "forbidden")
	reviewCount(t, db, "review_effects", 1)
	reviewCount(t, db, "operation_receipts", 1)
	if _, err := db.Exec("DELETE FROM capabilities"); err != nil {
		t.Fatal(err)
	}
	_, err = s.Store.Mutate(ctx, c.Token, op, allow, unexpectedApply)
	reviewCode(t, err, "unauthorized")
}

func TestSpecReviewRealDiskFullRollback(t *testing.T) {
	s, c := reviewService(t, t.TempDir())
	db := s.Store.db
	if _, err := db.Exec("CREATE TABLE review_effects (value BLOB)"); err != nil {
		t.Fatal(err)
	}
	var pages int
	if err := db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA max_page_count=" + strconv.Itoa(pages)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	op := Operation{ID: "review-diskfull-001", Name: "review", Scope: "fixture", Payload: json.RawMessage(`{}`)}
	allow := func(context.Context, *sql.Tx, Principal, Receipt) error { return nil }
	var sqliteFailure error
	_, err := s.Store.Mutate(ctx, c.Token, op, allow, func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		if _, err := tx.ExecContext(ctx, "INSERT INTO review_effects VALUES('partial')"); err != nil {
			return Receipt{}, err
		}
		_, sqliteFailure = tx.ExecContext(ctx, "INSERT INTO review_effects VALUES(zeroblob(1048576))")
		return Receipt{ResourceID: "fixture", Outcome: "created"}, sqliteFailure
	})
	var se sqlite3.Error
	if !errors.As(sqliteFailure, &se) || se.Code != sqlite3.ErrFull {
		t.Fatalf("did not generate real SQLITE_FULL: %v", sqliteFailure)
	}
	reviewCode(t, err, "storage_unavailable")
	reviewCount(t, db, "review_effects", 0)
	reviewCount(t, db, "operation_receipts", 0)
	if _, err := db.Exec("PRAGMA max_page_count=10000"); err != nil {
		t.Fatal(err)
	}
	_, err = s.Store.Mutate(ctx, c.Token, op, allow, func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		_, err := tx.ExecContext(ctx, "INSERT INTO review_effects VALUES('recovered')")
		return Receipt{ResourceID: "fixture", Outcome: "created"}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	reviewCount(t, db, "review_effects", 1)
	reviewCount(t, db, "operation_receipts", 1)
}

func TestSpecReviewMigrationRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	s := Store{db: db}
	ctx := context.Background()
	reviewCode(t, s.migrate(ctx, "CREATE TABLE partial(value); not valid SQL;"), "storage_unavailable")
	var version, n int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 0 {
		t.Fatalf("migration advanced version on failure: %d, %v", version, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='partial'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial migration persisted: %d, %v", n, err)
	}
	if err := s.migrate(ctx, migration1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE sentinel(value); INSERT INTO sentinel VALUES('preserved'); PRAGMA user_version=" + strconv.Itoa(schemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	_, err = openStore(ctx, path)
	reviewCode(t, err, "storage_unavailable")
	reviewCount(t, db, "sentinel", 1)
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != schemaVersion+1 {
		t.Fatalf("unsupported version changed: %d, %v", version, err)
	}
}

func TestSpecReviewBackupAndConfiguration(t *testing.T) {
	s, c := reviewService(t, t.TempDir())
	db := s.Store.db
	for pragma, want := range map[string]string{"journal_mode": "wal", "synchronous": "2", "foreign_keys": "1", "busy_timeout": "5000"} {
		var got string
		if err := db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil || got != want {
			t.Fatalf("PRAGMA %s=%s, want %s, error=%v", pragma, got, want, err)
		}
	}
	if !sqliteSupported(s.Store.sqliteVersion) || db.Stats().MaxOpenConnections != 1 {
		t.Fatal("unsupported engine or connection concurrency")
	}
	t.Logf("runtime SQLite %s", s.Store.sqliteVersion)
	if _, err := db.Exec("CREATE TABLE review_committed(value); INSERT INTO review_committed VALUES('from-WAL')"); err != nil {
		t.Fatal(err)
	}
	status, err := s.Store.Status()
	if err != nil || status.WALBytes == 0 {
		t.Fatalf("expected committed WAL data: %v", err)
	}
	backupDir := t.TempDir()
	if err := os.Chmod(backupDir, 0700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(backupDir, "backup.db")
	backupStatus, err := s.Store.Backup(context.Background(), c.Token, destination)
	if err != nil || backupStatus.DatabaseID != s.Store.id {
		t.Fatalf("backup failed: %v", err)
	}
	backup, err := sql.Open("sqlite3", "file:"+destination+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var integrity string
	if err := backup.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("backup integrity: %s, %v", integrity, err)
	}
	reviewCount(t, backup, "review_committed", 1)
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("backup permissions are not private")
	}
	_, err = s.Store.Backup(context.Background(), c.Token, destination)
	reviewCode(t, err, "conflict")
}

func reviewHTTP(t *testing.T, s *Service, token, method, path, body string, chunked bool) (int, string) {
	t.Helper()
	var reader io.Reader = strings.NewReader(body)
	if chunked {
		reader = io.NopCloser(reader)
	}
	r, err := http.NewRequest(method, s.Discovery.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-A-Stack-Instance", s.Discovery.InstanceID)
	r.Header.Set("Content-Type", "application/json")
	if r.ContentLength > MaxRequestBytes {
		r.Header.Set("Expect", "100-continue")
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true, ExpectContinueTimeout: time.Second}}
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var envelope struct{ Error *Error }
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error != nil {
		return resp.StatusCode, envelope.Error.Code
	}
	return resp.StatusCode, ""
}

func TestSpecReviewHTTPAndCapabilities(t *testing.T) {
	dir := t.TempDir()
	s, c := reviewService(t, dir)
	if _, err := NewClient(dir, "", AgentCapability); err == nil {
		t.Fatal("Agent mode fell back to Operator credential")
	}
	agent := issueTest(t, s.Store, AgentCapability, "review-agent", "review-session")
	browser := issueTest(t, s.Store, BrowserCapability, "review-browser", "review-session")
	for _, tc := range []struct {
		name, token, path string
		want              int
	}{
		{"operator-health", c.Token, "/v1/health", 200},
		{"invalid-credential", opaqueID(), "/v1/health", 401},
		{"agent-health", agent.Token, "/v1/health", 200},
		{"agent-storage", agent.Token, "/v1/storage", 403},
		{"browser-health", browser.Token, "/v1/health", 403},
		{"browser-storage", browser.Token, "/v1/storage", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := reviewHTTP(t, s, tc.token, "GET", tc.path, "", false)
			if status != tc.want {
				t.Fatalf("status=%d, want=%d", status, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name, body string
		chunked    bool
	}{
		{"malformed", "{", false},
		{"null", "null", false},
		{"unknown-field", `{"extra":true}`, false},
		{"invalid-UTF8", "{\"destination\":\"\xff\"}", false},
		{"oversize-known", strings.Repeat("x", MaxRequestBytes+1), false},
		{"oversize-chunked", strings.Repeat("x", MaxRequestBytes+1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, code := reviewHTTP(t, s, c.Token, "POST", "/v1/storage/backup", tc.body, tc.chunked)
			if status != 400 || code != "invalid_input" {
				t.Fatalf("status=%d, code=%s", status, code)
			}
		})
	}
}

func TestSpecReviewOversizedChunkedGET(t *testing.T) {
	s, c := reviewService(t, t.TempDir())
	for _, path := range []string{"/v1/health", "/v1/storage"} {
		t.Run(path, func(t *testing.T) {
			status, code := reviewHTTP(t, s, c.Token, "GET", path, strings.Repeat("x", MaxRequestBytes+1), true)
			if status < 400 || code != "invalid_input" {
				t.Fatalf("oversized chunked GET succeeded: status=%d, code=%s", status, code)
			}
		})
	}
}
