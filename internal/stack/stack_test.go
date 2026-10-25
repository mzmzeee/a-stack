package stack

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var ctx = context.Background()

func privateTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
}
func startTest(t *testing.T) (*Service, string, Credential) {
	t.Helper()
	dir := privateTestDir(t)
	s, err := Start(ctx, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve() }()
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := s.Close(c); err != nil {
			t.Error(err)
		}
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	credential, err := ReadCredential(filepath.Join(dir, "operator.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s, dir, credential
}
func issueTest(t *testing.T, s *Store, kind CapabilityKind, subject, session string) Credential {
	t.Helper()
	c := Credential{Kind: kind, Token: opaqueID()}
	execTest(t, s, "INSERT INTO capabilities(token_hash,kind,subject_id,session_id) VALUES(?,?,?,?)", tokenHash(c.Token), kind, subject, session)
	return c
}
func execTest(t *testing.T, s *Store, query string, args ...any) {
	t.Helper()
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		t.Fatal(err)
	}
}
func countTest(t *testing.T, s *Store, query string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
func rawRequest(t *testing.T, s *Service, credential Credential, method, path string, body io.Reader, headers map[string]string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, s.Discovery.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-A-Stack-Instance", s.Discovery.InstanceID)
	req.Header.Set("Authorization", "Bearer "+credential.Token)
	for key, value := range headers {
		if key == "Host" {
			req.Host = value
		} else {
			req.Header.Set(key, value)
		}
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, data
}
func TestServiceSecurityRestartAndSQLite(t *testing.T) {
	s, dir, c := startTest(t)
	original, err := os.ReadFile(filepath.Join(dir, "operator.json"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	data, err := client.Do(ctx, "GET", "/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	var health struct {
		Data Health `json:"data"`
	}
	if json.Unmarshal(data, &health) != nil || health.Data.DatabaseID != s.Store.id || !sqliteSupported(health.Data.SQLiteVersion) {
		t.Fatal("invalid health")
	}
	t.Logf("runtime SQLite %s, schema %d", health.Data.SQLiteVersion, health.Data.SchemaVersion)
	for pragma, want := range map[string]string{"journal_mode": "wal", "synchronous": "2", "foreign_keys": "1", "busy_timeout": "5000"} {
		var got string
		if s.Store.db.QueryRow("PRAGMA "+pragma).Scan(&got) != nil || got != want {
			t.Fatalf("%s=%s want %s", pragma, got, want)
		}
	}
	if s.Store.db.Stats().MaxOpenConnections != 1 {
		t.Fatal("database is not serialized")
	}
	for _, name := range []string{"operator.json", "service.json", "service.lock", "state.db", "state.db-wal", "state.db-shm"} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("%s mode=%v", name, info.Mode())
		}
	}
	_, err = Start(ctx, dir, "")
	requireCode(t, err, "conflict")
	for _, address := range []string{"0.0.0.0:0", "localhost:0", "[::1]:0", "127.0.0.2:0"} {
		_, err := Start(ctx, privateTestDir(t), address)
		requireCode(t, err, "invalid_input")
	}
	cases := []struct {
		name, method, path, body, code string
		headers                        map[string]string
	}{
		{"unauthorized", "GET", "/v1/health", "", "unauthorized", map[string]string{"Authorization": ""}},
		{"host", "GET", "/v1/health", "", "forbidden", map[string]string{"Host": "evil.example"}},
		{"origin", "GET", "/v1/health", "", "forbidden", map[string]string{"Origin": "https://evil.example"}},
		{"instance", "GET", "/v1/health", "", "conflict", map[string]string{"X-A-Stack-Instance": "old"}},
		{"query token", "GET", "/v1/health?token=do-not-log", "", "invalid_input", nil},
		{"malformed", "POST", "/v1/storage/backup", "{", "invalid_input", nil},
		{"unknown", "POST", "/v1/storage/backup", `{"unknown":true}`, "invalid_input", nil},
		{"null", "POST", "/v1/storage/backup", "null", "invalid_input", nil},
		{"multiple", "POST", "/v1/storage/backup", "{} {}", "invalid_input", nil},
		{"utf8", "POST", "/v1/storage/backup", string([]byte{'{', '"', 'x', '"', ':', '"', 255, '"', '}'}), "invalid_input", nil},
		{"oversize", "POST", "/v1/storage/backup", strings.Repeat("x", MaxRequestBytes+1), "invalid_input", nil},
		{"wrong method", "POST", "/v1/health", "{}", "invalid_input", nil},
		{"missing route", "GET", "/v1/unknown", "", "not_found", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.headers == nil {
				tc.headers = map[string]string{}
			}
			tc.headers["Content-Type"] = "application/json"
			status, data := rawRequest(t, s, c, tc.method, tc.path, strings.NewReader(tc.body), tc.headers)
			var envelope struct {
				Error *Error `json:"error"`
			}
			if json.Unmarshal(data, &envelope) != nil || envelope.Error == nil || envelope.Error.Code != tc.code || status < 400 {
				t.Fatalf("status=%d data=%s", status, data)
			}
			if bytes.Contains(data, []byte(c.Token)) || bytes.Contains(data, []byte("do-not-log")) {
				t.Fatal("secret reflected")
			}
		})
	}
	// Unknown-length/chunked GET bodies must be rejected too, before dispatch.
	for _, route := range []struct{ method, path string }{{"GET", "/v1/health"}, {"POST", "/v1/storage/backup"}} {
		status, body := rawRequest(t, s, c, route.method, route.path, io.NopCloser(strings.NewReader(strings.Repeat("x", MaxRequestBytes+1))), map[string]string{"Content-Type": "application/json"})
		if status != 400 || !bytes.Contains(body, []byte("invalid_input")) {
			t.Fatal("chunked body accepted")
		}
	}
	oldID := s.Store.id
	oldInstance := s.Discovery.InstanceID
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = ReadDiscovery(dir)
	requireCode(t, err, "storage_unavailable")
	restarted, err := Start(ctx, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(ctx)
	if restarted.Store.id != oldID || restarted.Discovery.InstanceID == oldInstance {
		t.Fatal("restart identity failure")
	}
	after, err := os.ReadFile(filepath.Join(dir, "operator.json"))
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("Operator credential changed on restart")
	}
	if _, err = authenticate(ctx, restarted.Store.db, c.Token); err != nil {
		t.Fatal(err)
	}
}
func TestCapabilitiesAndPrivateFiles(t *testing.T) {
	s, dir, operator := startTest(t)
	agent := issueTest(t, s.Store, AgentCapability, "agent-a", "session-a")
	browser := issueTest(t, s.Store, BrowserCapability, "view-a", "session-a")
	status, _ := rawRequest(t, s, agent, "GET", "/v1/health", nil, nil)
	if status != 200 {
		t.Fatal("agent health")
	}
	for _, c := range []Credential{agent, browser} {
		status, _ := rawRequest(t, s, c, "POST", "/v1/storage/backup", strings.NewReader(`{}`), map[string]string{"Content-Type": "application/json"})
		if status != 403 {
			t.Fatal("non-Operator backup allowed")
		}
		status, _ = rawRequest(t, s, c, "GET", "/v1/storage", nil, nil)
		if status != 403 {
			t.Fatal("non-Operator storage inspect allowed")
		}
	}
	status, _ = rawRequest(t, s, browser, "GET", "/v1/health", nil, nil)
	if status != 403 {
		t.Fatal("browser global data")
	}
	_, err := NewClient(dir, "", AgentCapability)
	requireCode(t, err, "invalid_input")
	_, err = NewClient(dir, filepath.Join(dir, "operator.json"), AgentCapability)
	requireCode(t, err, "forbidden")
	agentPath := filepath.Join(dir, "agent.json")
	if err = writePrivate(agentPath, agent, false); err != nil {
		t.Fatal(err)
	}
	if _, err = NewClient(dir, agentPath, AgentCapability); err != nil {
		t.Fatal(err)
	}
	execTest(t, s.Store, "DELETE FROM capabilities WHERE token_hash=?", tokenHash(agent.Token))
	status, _ = rawRequest(t, s, agent, "GET", "/v1/health", nil, nil)
	if status != 401 {
		t.Fatal("revoked credential accepted")
	}
	var hash string
	if err = s.Store.db.QueryRow("SELECT token_hash FROM capabilities WHERE kind='operator'").Scan(&hash); err != nil || hash == operator.Token || hash != tokenHash(operator.Token) {
		t.Fatal("credential not hashed")
	}
	if os.Chmod(agentPath, 0644) != nil {
		t.Fatal("chmod")
	}
	_, err = ReadCredential(agentPath)
	requireCode(t, err, "invalid_input")
	link := filepath.Join(dir, "linked.json")
	if os.Symlink(filepath.Join(dir, "operator.json"), link) != nil {
		t.Fatal("symlink")
	}
	_, err = ReadCredential(link)
	requireCode(t, err, "invalid_input")
	unsafe := privateTestDir(t)
	if os.Chmod(unsafe, 0755) != nil {
		t.Fatal("chmod")
	}
	_, err = Start(ctx, unsafe, "")
	requireCode(t, err, "invalid_input")
	root := privateTestDir(t)
	if os.Mkdir(filepath.Join(root, ".git"), 0700) != nil {
		t.Fatal("mkdir")
	}
	_, err = Start(ctx, filepath.Join(root, "private"), "")
	requireCode(t, err, "invalid_input")
}
func TestMigrationFailureAndSchemaGuards(t *testing.T) {
	path := filepath.Join(privateTestDir(t), "state.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path}
	err = s.migrate(ctx, "CREATE TABLE partial(value TEXT); SELECT nonexistent_function();")
	requireCode(t, err, "storage_unavailable")
	if countTest(t, s, "SELECT count(*) FROM sqlite_schema WHERE name='partial'") != 0 || countTest(t, s, "PRAGMA user_version") != 0 {
		t.Fatal("partial migration survived")
	}
	if err = s.migrate(ctx, migration1); err != nil {
		t.Fatal(err)
	}
	var identity string
	if db.QueryRow("SELECT value FROM metadata WHERE key='database_id'").Scan(&identity) != nil {
		t.Fatal("identity")
	}
	execTest(t, s, fmt.Sprintf("PRAGMA user_version=%d", schemaVersion+1))
	err = s.migrate(ctx, migration1)
	requireCode(t, err, "storage_unavailable")
	var after string
	if db.QueryRow("SELECT value FROM metadata WHERE key='database_id'").Scan(&after) != nil || after != identity {
		t.Fatal("unsupported schema changed")
	}
	execTest(t, s, "PRAGMA user_version=1; PRAGMA application_id=123")
	err = s.migrate(ctx, migration1)
	requireCode(t, err, "storage_unavailable")
	for version, want := range map[string]bool{"3.51.2": false, "3.51.3": true, "3.53.4": true, "4.0.0": true, "3.50.9": false, "garbage": false} {
		if sqliteSupported(version) != want {
			t.Fatalf("version guard %s", version)
		}
	}
}

// These tables exist only in temporary tests. They exercise transactions without
// introducing placeholder Workspace/Session/Task implementations in the product.
func transactionFixture(t *testing.T) (*Store, Credential) {
	t.Helper()
	service, _, _ := startTest(t)
	s := service.Store
	c := issueTest(t, s, AgentCapability, "agent", "session")
	execTest(t, s, `CREATE TABLE test_objects(id TEXT PRIMARY KEY, value TEXT); CREATE TABLE test_access(allowed INTEGER, closed INTEGER); INSERT INTO test_access VALUES(1,0); CREATE TABLE test_history(object_id TEXT);`)
	return s, c
}
func replayAccess(ctx context.Context, tx *sql.Tx, p Principal, r Receipt) error {
	var allowed int
	if tx.QueryRowContext(ctx, "SELECT allowed FROM test_access").Scan(&allowed) != nil {
		return storageError()
	}
	if allowed == 0 {
		return failure("forbidden", "current read access required")
	}
	return nil
}
func testApply(value string, fail bool) func(context.Context, *sql.Tx, Principal) (Receipt, error) {
	return func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		var allowed, closed int
		if tx.QueryRowContext(ctx, "SELECT allowed,closed FROM test_access").Scan(&allowed, &closed) != nil {
			return Receipt{}, storageError()
		}
		if allowed == 0 {
			return Receipt{}, failure("forbidden", "membership required")
		}
		if closed != 0 {
			return Receipt{}, failure("session_closed", "Session closed")
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO test_objects VALUES('object',?)", value); err != nil {
			return Receipt{}, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO test_history VALUES('object')"); err != nil {
			return Receipt{}, err
		}
		if fail {
			return Receipt{}, failure("storage_unavailable", "injected precommit failure")
		}
		return Receipt{ResourceID: "object", Revision: 1, Outcome: "created"}, nil
	}
}
func TestLostResponseReplayReauthorizationAndRollback(t *testing.T) {
	s, c := transactionFixture(t)
	op := Operation{ID: "operation-123", Name: "test.create", Scope: "session", Payload: json.RawMessage(`{"value":"stored-once"}`)}
	// The committed response is deliberately discarded, as with a lost connection.
	original, err := s.Mutate(ctx, c.Token, op, replayAccess, testApply("stored-once", false))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	receipts := make(chan Receipt, 16)
	failures := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.Mutate(ctx, c.Token, op, replayAccess, testApply("duplicate", false))
			if err != nil {
				failures <- err
			} else {
				receipts <- r
			}
		}()
	}
	wg.Wait()
	close(receipts)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	for r := range receipts {
		if r != original {
			t.Fatal("receipt changed")
		}
	}
	if countTest(t, s, "SELECT count(*) FROM test_objects") != 1 || countTest(t, s, "SELECT count(*) FROM test_history") != 1 || countTest(t, s, "SELECT count(*) FROM operation_receipts") != 1 {
		t.Fatal("retry duplicated data")
	}
	changed := op
	changed.Payload = json.RawMessage(`{"value":"changed"}`)
	_, err = s.Mutate(ctx, c.Token, changed, replayAccess, testApply("x", false))
	requireCode(t, err, "operation_conflict")
	changed = op
	changed.Scope = "other-session"
	_, err = s.Mutate(ctx, c.Token, changed, replayAccess, testApply("x", false))
	requireCode(t, err, "operation_conflict")
	execTest(t, s, "UPDATE test_access SET closed=1")
	recovered, err := s.Mutate(ctx, c.Token, op, replayAccess, testApply("x", false))
	if err != nil || recovered != original {
		t.Fatal("closed Session lost replay")
	}
	changed = op
	changed.ID = "operation-new"
	_, err = s.Mutate(ctx, c.Token, changed, replayAccess, testApply("x", false))
	requireCode(t, err, "session_closed")
	execTest(t, s, "UPDATE test_access SET allowed=0")
	_, err = s.Mutate(ctx, c.Token, op, replayAccess, testApply("x", false))
	requireCode(t, err, "forbidden")
	execTest(t, s, "DELETE FROM capabilities WHERE token_hash=?", tokenHash(c.Token))
	_, err = s.Mutate(ctx, c.Token, op, replayAccess, testApply("x", false))
	requireCode(t, err, "unauthorized")
	// A different actor cannot reuse somebody else's cached receipt.
	other := issueTest(t, s, AgentCapability, "other-agent", "session")
	_, err = s.Mutate(ctx, other.Token, op, replayAccess, testApply("x", false))
	requireCode(t, err, "forbidden")
}
func TestMutationAtomicFailureAndDiskFull(t *testing.T) {
	s, c := transactionFixture(t)
	op := Operation{ID: "operation-failed", Name: "test", Scope: "session", Payload: json.RawMessage(`{}`)}
	_, err := s.Mutate(ctx, c.Token, op, replayAccess, testApply("rollback", true))
	requireCode(t, err, "storage_unavailable")
	if countTest(t, s, "SELECT count(*) FROM test_objects") != 0 || countTest(t, s, "SELECT count(*) FROM test_history") != 0 || countTest(t, s, "SELECT count(*) FROM operation_receipts") != 0 {
		t.Fatal("partial precommit data")
	}
	execTest(t, s, "CREATE TRIGGER reject_receipt BEFORE INSERT ON operation_receipts BEGIN SELECT RAISE(ABORT,'injected receipt failure'); END")
	_, err = s.Mutate(ctx, c.Token, op, replayAccess, testApply("rollback", false))
	requireCode(t, err, "storage_unavailable")
	if countTest(t, s, "SELECT count(*) FROM test_objects") != 0 || countTest(t, s, "SELECT count(*) FROM test_history") != 0 {
		t.Fatal("receipt failure did not rollback mutation")
	}
	execTest(t, s, "DROP TRIGGER reject_receipt; CREATE TABLE test_blob(data BLOB)")
	pages := countTest(t, s, "PRAGMA page_count")
	execTest(t, s, fmt.Sprintf("PRAGMA max_page_count=%d", pages))
	var sqliteErr error
	_, err = s.Mutate(ctx, c.Token, op, replayAccess, func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		if _, err := tx.ExecContext(ctx, "INSERT INTO test_objects VALUES('partial','rollback')"); err != nil {
			return Receipt{}, err
		}
		_, sqliteErr = tx.ExecContext(ctx, "INSERT INTO test_blob VALUES(zeroblob(4194304))")
		return Receipt{}, sqliteErr
	})
	if !isSQLiteFull(sqliteErr) {
		t.Fatalf("expected real SQLITE_FULL, got %v", sqliteErr)
	}
	requireCode(t, err, "storage_unavailable")
	if countTest(t, s, "SELECT count(*) FROM test_objects") != 0 || countTest(t, s, "SELECT count(*) FROM test_blob") != 0 || countTest(t, s, "SELECT count(*) FROM operation_receipts") != 0 {
		t.Fatal("disk-full left partial data")
	}
	execTest(t, s, "PRAGMA max_page_count=1073741823")
	if _, err = s.Mutate(ctx, c.Token, op, replayAccess, testApply("recovered", false)); err != nil {
		t.Fatal("disk-full recovery:", err)
	}
}
func TestSafeBackupRestoreAndMissingCredential(t *testing.T) {
	s, dir, c := startTest(t)
	execTest(t, s.Store, "CREATE TABLE test_retained(value TEXT)")
	op := Operation{ID: "backup-operation", Name: "test", Scope: "retained", Payload: json.RawMessage(`{}`)}
	readAccess := func(ctx context.Context, tx *sql.Tx, p Principal, receipt Receipt) error {
		var value string
		if err := tx.QueryRowContext(ctx, "SELECT value FROM test_retained").Scan(&value); err != nil {
			return err
		}
		return nil
	}
	original, err := s.Store.Mutate(ctx, c.Token, op, readAccess, func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		_, err := tx.ExecContext(ctx, "INSERT INTO test_retained VALUES('retained')")
		return Receipt{ResourceID: "retained", Revision: 1, Outcome: "created"}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(privateTestDir(t), "backup.db")
	client, err := NewClient(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Do(ctx, "POST", "/v1/storage/backup", map[string]string{"destination": backupPath}); err != nil {
		t.Fatal(err)
	}
	_, err = s.Store.Backup(ctx, c.Token, backupPath)
	requireCode(t, err, "conflict")
	info, err := os.Stat(backupPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("backup permissions")
	}
	status, err := s.Store.Status()
	if err != nil || status.DatabaseBytes == 0 || status.WALBytes == 0 {
		t.Fatal("storage sizes")
	}
	t.Logf("foundation + one retained test row: db=%d wal=%d shm=%d backup=%d bytes", status.DatabaseBytes, status.WALBytes, status.SHMBytes, info.Size())
	restoredDir := privateTestDir(t)
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if os.WriteFile(filepath.Join(restoredDir, "state.db"), backup, 0600) != nil {
		t.Fatal("copy completed backup")
	}
	if err = writePrivate(filepath.Join(restoredDir, "operator.json"), c, false); err != nil {
		t.Fatal(err)
	}
	restored, err := Start(ctx, restoredDir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close(ctx)
	var integrity, value string
	if restored.Store.db.QueryRow("PRAGMA integrity_check").Scan(&integrity) != nil || integrity != "ok" || restored.Store.db.QueryRow("SELECT value FROM test_retained").Scan(&value) != nil || value != "retained" || restored.Store.id != s.Store.id {
		t.Fatal("restore failed")
	}
	replayed, err := restored.Store.Mutate(ctx, c.Token, op, readAccess, func(context.Context, *sql.Tx, Principal) (Receipt, error) {
		t.Error("restored receipt applied twice")
		return Receipt{}, storageError()
	})
	if err != nil || replayed != original {
		t.Fatal("backup lost original retry receipt", err)
	}
	// A missing established credential must never silently mint a replacement.
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if os.Remove(filepath.Join(dir, "operator.json")) != nil {
		t.Fatal("remove Operator file")
	}
	_, err = Start(ctx, dir, "")
	requireCode(t, err, "storage_unavailable")
	if _, err = os.Stat(filepath.Join(dir, "operator.json")); !os.IsNotExist(err) {
		t.Fatal("credential silently regenerated")
	}
	if err = writePrivate(filepath.Join(dir, "operator.json"), c, false); err != nil {
		t.Fatal(err)
	}
	recovered, err := Start(ctx, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close(ctx)
	if recovered.Store.id != s.Store.id {
		t.Fatal("recovery changed identity")
	}
}
func TestBootstrapInterruptedBeforeCredentialCommit(t *testing.T) {
	dir := privateTestDir(t)
	lock, err := lockState(dir)
	if err != nil {
		t.Fatal(err)
	}
	path, err := ensureDatabaseFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := openStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	pending := Credential{Kind: OperatorCapability, Token: opaqueID()}
	if err = writePrivate(filepath.Join(dir, "operator.json"), pending, false); err != nil {
		t.Fatal(err)
	}
	s.db.Close()
	lock.close()
	service, err := Start(ctx, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(ctx)
	if _, err = authenticate(ctx, service.Store.db, pending.Token); err != nil {
		t.Fatal("pending credential not recovered")
	}
}
func TestReadOnlyStorageFailure(t *testing.T) {
	service, _, c := startTest(t)
	s := service.Store
	db, err := sql.Open("sqlite3", (&url.URL{Scheme: "file", Path: s.path}).String()+"?mode=ro&_txlock=immediate&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	readOnly := &Store{db: db, path: s.path}
	_, err = readOnly.Mutate(ctx, c.Token, Operation{ID: "operation-readonly", Name: "test", Scope: "local", Payload: json.RawMessage(`{}`)}, func(context.Context, *sql.Tx, Principal, Receipt) error { return nil }, func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		_, err := tx.ExecContext(ctx, "INSERT INTO metadata VALUES('cannot-write','value')")
		return Receipt{}, err
	})
	requireCode(t, err, "storage_unavailable")
	if countTest(t, s, "SELECT count(*) FROM operation_receipts") != 0 {
		t.Fatal("read-only failure receipt")
	}
}

func TestForeignSchemaLiteralSQLitePrefix(t *testing.T) {
	for _, table := range []string{"sqliteXforeign", "foreign_table"} {
		t.Run(table, func(t *testing.T) {
			dir := privateTestDir(t)
			path, err := ensureDatabaseFile(dir)
			if err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec("CREATE TABLE " + table + "(value TEXT); INSERT INTO " + table + " VALUES('preserve')"); err != nil {
				t.Fatal(err)
			}
			_, err = Start(ctx, dir, "")
			requireCode(t, err, "storage_unavailable")
			var version, app, count int
			var value string
			if db.QueryRow("PRAGMA user_version").Scan(&version) != nil || version != 0 || db.QueryRow("PRAGMA application_id").Scan(&app) != nil || app != 0 {
				t.Fatal("foreign database version changed")
			}
			if db.QueryRow("SELECT value FROM "+table).Scan(&value) != nil || value != "preserve" {
				t.Fatal("foreign data changed")
			}
			if db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name IN ('metadata','capabilities','operation_receipts')").Scan(&count) != nil || count != 0 {
				t.Fatal("foreign database migrated")
			}
		})
	}
}
func TestFIFOCredentialFailsWithoutBlocking(t *testing.T) {
	path := filepath.Join(privateTestDir(t), "fifo.json")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := ReadCredential(path); done <- err }()
	select {
	case err := <-done:
		requireCode(t, err, "invalid_input")
	case <-time.After(time.Second):
		t.Fatal("FIFO credential blocked")
	}
}
func TestReceiptSurvivesDatabaseRestart(t *testing.T) {
	service, dir, c := startTest(t)
	s := service.Store
	execTest(t, s, "CREATE TABLE test_access(allowed INTEGER, closed INTEGER); INSERT INTO test_access VALUES(1,0); CREATE TABLE test_objects(id TEXT PRIMARY KEY,value TEXT); CREATE TABLE test_history(object_id TEXT)")
	op := Operation{ID: "restart-operation", Name: "test", Scope: "local", Payload: json.RawMessage(`{}`)}
	original, err := s.Mutate(ctx, c.Token, op, replayAccess, testApply("single body", false))
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	restarted, err := Start(ctx, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(ctx)
	receipt, err := restarted.Store.Mutate(ctx, c.Token, op, replayAccess, testApply("duplicate", false))
	if err != nil || receipt != original {
		t.Fatal("restart replay changed receipt", err)
	}
	if countTest(t, restarted.Store, "SELECT count(*) FROM test_objects") != 1 || countTest(t, restarted.Store, "SELECT count(*) FROM operation_receipts") != 1 {
		t.Fatal("restart duplicated mutation")
	}
	browser := issueTest(t, restarted.Store, BrowserCapability, "view", "session")
	_, err = restarted.Store.Mutate(ctx, browser.Token, op, replayAccess, testApply("forbidden", false))
	requireCode(t, err, "forbidden")
}

func TestTuneServiceGCRespectsGOGC(t *testing.T) {
	previous := debug.SetGCPercent(100)
	defer debug.SetGCPercent(previous)
	t.Setenv("GOGC", "150")
	TuneServiceGC()
	if got := debug.SetGCPercent(100); got != 100 {
		t.Fatal("TuneServiceGC overrode GOGC from the environment", got)
	}
	os.Unsetenv("GOGC")
	TuneServiceGC()
	if got := debug.SetGCPercent(100); got != ServiceGCPercent {
		t.Fatal("TuneServiceGC did not set the service GC target", got)
	}
}

// The driver caches prepared statements per connection. openStore closes the
// connection that ran the migrations, so no statement prepared against the
// old schema survives into the service; the next query opens a fresh one.
func TestStatementCacheStartsAfterMigrations(t *testing.T) {
	dir := privateTestDir(t)
	path, err := ensureDatabaseFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := openStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if stats := s.db.Stats(); stats.MaxIdleClosed != 1 {
		t.Fatalf("migration connection kept open: %+v", stats)
	}
	var version int
	for i := 0; i < 3; i++ {
		if err = s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != schemaVersion {
			t.Fatal("fresh connection after migrations", version, err)
		}
	}
	if stats := s.db.Stats(); stats.OpenConnections != 1 || stats.MaxIdleClosed != 1 {
		t.Fatalf("service did not reuse its one connection: %+v", stats)
	}
}
