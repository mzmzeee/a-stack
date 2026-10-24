package stack

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const schemaVersion = 5
const applicationID = 0x4153544b
const migration1 = `
CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE capabilities (
 token_hash TEXT PRIMARY KEY,
 kind TEXT NOT NULL CHECK(kind IN ('operator','agent','browser')),
 subject_id TEXT NOT NULL,
 session_id TEXT NOT NULL,
 CHECK ((kind='operator' AND session_id='') OR (kind!='operator' AND session_id!=''))
) WITHOUT ROWID;
CREATE INDEX capabilities_subject ON capabilities(kind,subject_id);
CREATE TABLE operation_receipts (
 actor TEXT NOT NULL, operation_id TEXT NOT NULL,
 name TEXT NOT NULL, scope TEXT NOT NULL, digest TEXT NOT NULL,
 resource_id TEXT NOT NULL, revision INTEGER NOT NULL,
 outcome TEXT NOT NULL, body_ref TEXT NOT NULL, received_at TEXT NOT NULL,
 PRIMARY KEY(actor,operation_id)
) WITHOUT ROWID;
`

type Store struct {
	db            *sql.DB
	path          string
	id            string
	sqliteVersion string
	noticeWake    func()
}

func openStore(ctx context.Context, path string) (*Store, error) {
	u := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite3", u.String()+"?_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on&_busy_timeout=5000&_txlock=immediate")
	if err != nil {
		return nil, storageError()
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, path: path}
	ok := false
	defer func() {
		if !ok {
			db.Close()
		}
	}()
	if err = db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&s.sqliteVersion); err != nil {
		return nil, storageError()
	}
	if !sqliteSupported(s.sqliteVersion) {
		return nil, failure("storage_unavailable", "SQLite >=3.51.3 is required")
	}
	if err = s.migrate(ctx, migration1); err != nil {
		return nil, err
	}
	if err = s.recoverNotices(ctx); err != nil {
		return nil, err
	}
	if err = db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='database_id'").Scan(&s.id); err != nil || !validToken(s.id) {
		return nil, storageError()
	}
	ok = true
	return s, nil
}
func sqliteSupported(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil {
			return false
		}
		n[i] = v
	}
	for i, v := range [3]int{3, 51, 3} {
		if n[i] > v {
			return true
		}
		if n[i] < v {
			return false
		}
	}
	return true
}
func (s *Store) migrate(ctx context.Context, migration string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError()
	}
	defer tx.Rollback()
	var version, app int
	if tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version) != nil || tx.QueryRowContext(ctx, "PRAGMA application_id").Scan(&app) != nil {
		return storageError()
	}
	if version > schemaVersion || version < 0 || (version > 0 && app != applicationID) || (version == 0 && app != 0) {
		return failure("storage_unavailable", "unsupported database schema; restore or use a compatible binary")
	}
	if version == 0 {
		var count int
		if tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name NOT GLOB 'sqlite_*'").Scan(&count) != nil {
			return storageError()
		}
		if count != 0 {
			return failure("storage_unavailable", "unrecognized database schema")
		}
		if _, err = tx.ExecContext(ctx, migration); err != nil {
			return storageError()
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES('database_id',?)", opaqueID()); err != nil {
			return storageError()
		}
		if _, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=1; PRAGMA application_id=%d", applicationID)); err != nil {
			return storageError()
		}
		version = 1
	}
	if version < 2 {
		if _, err = tx.ExecContext(ctx, migration2); err != nil {
			return storageError()
		}
		if _, err = tx.ExecContext(ctx, "PRAGMA user_version=2"); err != nil {
			return storageError()
		}
	}
	if version < 3 {
		if _, err = tx.ExecContext(ctx, migration3); err != nil {
			return storageError()
		}
		if _, err = tx.ExecContext(ctx, "PRAGMA user_version=3"); err != nil {
			return storageError()
		}
	}
	if version < 4 {
		if _, err = tx.ExecContext(ctx, migration4); err != nil {
			return storageError()
		}
		if _, err = tx.ExecContext(ctx, "PRAGMA user_version=4"); err != nil {
			return storageError()
		}
	}
	if version < 5 {
		if _, err = tx.ExecContext(ctx, migration5); err != nil {
			return storageError()
		}
		if _, err = tx.ExecContext(ctx, "PRAGMA user_version=5"); err != nil {
			return storageError()
		}
	}
	if err = tx.Commit(); err != nil {
		return storageError()
	}
	return nil
}

type CapabilityKind string

const (
	OperatorCapability CapabilityKind = "operator"
	AgentCapability    CapabilityKind = "agent"
	BrowserCapability  CapabilityKind = "browser"
)

func validKind(k CapabilityKind) bool {
	return k == OperatorCapability || k == AgentCapability || k == BrowserCapability
}

type Principal struct {
	Kind      CapabilityKind
	SubjectID string
	SessionID string
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// IssueCapability participates in the caller's registration/rebind transaction.
// Only the hash persists; callers must deliver the returned credential privately.
func IssueCapability(ctx context.Context, tx *sql.Tx, kind CapabilityKind, subject, session string) (Credential, error) {
	if !validKind(kind) || subject == "" || len(subject) > 128 || len(session) > 128 || (kind == OperatorCapability && session != "") || (kind != OperatorCapability && session == "") {
		return Credential{}, failure("invalid_input", "invalid credential scope")
	}
	c := Credential{Kind: kind, Token: opaqueID()}
	_, err := tx.ExecContext(ctx, "INSERT INTO capabilities(token_hash,kind,subject_id,session_id) VALUES(?,?,?,?)", tokenHash(c.Token), kind, subject, session)
	if err != nil {
		return Credential{}, storageError()
	}
	return c, nil
}
func authenticate(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, token string) (Principal, error) {
	var p Principal
	if !validToken(token) {
		return p, failure("unauthorized", "valid bearer credential required")
	}
	err := q.QueryRowContext(ctx, "SELECT kind,subject_id,session_id FROM capabilities WHERE token_hash=?", tokenHash(token)).Scan(&p.Kind, &p.SubjectID, &p.SessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return p, failure("unauthorized", "valid bearer credential required")
	}
	if err != nil {
		return p, storageError()
	}
	return p, nil
}
func (s *Store) ensureOperator(ctx context.Context, dir string) error {
	var count int
	if s.db.QueryRowContext(ctx, "SELECT count(*) FROM capabilities WHERE kind='operator'").Scan(&count) != nil {
		return storageError()
	}
	path := filepath.Join(dir, "operator.json")
	_, statErr := os.Lstat(path)
	if count == 0 && os.IsNotExist(statErr) {
		if err := writePrivate(path, Credential{Kind: OperatorCapability, Token: opaqueID()}, false); err != nil {
			return err
		}
	}
	c, err := ReadCredential(path)
	if err != nil {
		return failure("storage_unavailable", "Operator credential missing or unsafe; restore the original private credential file")
	}
	if c.Kind != OperatorCapability {
		return failure("storage_unavailable", "invalid Operator credential")
	}
	if count == 0 {
		// File-first bootstrap is restartable after a crash; never rotate an
		// established credential merely because a local file disappeared.
		_, err = s.db.ExecContext(ctx, "INSERT INTO capabilities(token_hash,kind,subject_id,session_id) VALUES(?,'operator',?,'')", tokenHash(c.Token), opaqueID())
		if err != nil {
			return storageError()
		}
	}
	p, err := authenticate(ctx, s.db, c.Token)
	if err != nil || p.Kind != OperatorCapability {
		return failure("storage_unavailable", "Operator credential does not match this database")
	}
	return nil
}

type Operation struct {
	ID      string
	Name    string
	Scope   string
	Payload json.RawMessage
}

// Receipt contains references and scalar outcomes only; never put a body or
// snapshot in Outcome, ResourceID or BodyRef. Later slices persist bodies once.
type Receipt struct {
	OperationID string `json:"operation_id"`
	ResourceID  string `json:"resource_id"`
	Revision    int64  `json:"revision"`
	Outcome     string `json:"outcome"`
	BodyRef     string `json:"body_ref,omitempty"`
	ReceivedAt  string `json:"received_at"`
}

// Mutate reauthenticates inside the serialized transaction. Replay authorization
// is mandatory and distinct from write authorization: it must check current read
// access even after closure/removal for a matching digest. Mismatches return only
// operation_conflict after authentication, without invoking typed replay or
// exposing the saved receipt. apply owns current role, openness, reference,
// expected revision checks, mutation and history/mention inserts in this same tx.
// Both callbacks must do database work only, with no network or terminal calls.
func (s *Store) Mutate(ctx context.Context, token string, op Operation,
	authorizeReplay func(context.Context, *sql.Tx, Principal, Receipt) error,
	apply func(context.Context, *sql.Tx, Principal) (Receipt, error)) (Receipt, error) {
	empty := Receipt{}
	if len(op.ID) < 8 || len(op.ID) > 128 || len(op.Name) == 0 || len(op.Name) > 64 || len(op.Scope) == 0 || len(op.Scope) > 128 || len(op.Payload) > MaxRequestBytes || !json.Valid(op.Payload) || authorizeReplay == nil || apply == nil {
		return empty, failure("invalid_input", "operation ID, bounded payload, scope and authorization callbacks required")
	}
	if !validOperationID(op.ID) {
		return empty, failure("invalid_input", "invalid operation ID")
	}
	b, _ := json.Marshal(struct {
		Name, Scope string
		Payload     json.RawMessage
	}{op.Name, op.Scope, op.Payload})
	sum := sha256.Sum256(b)
	digest := hex.EncodeToString(sum[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, storageError()
	}
	defer tx.Rollback()
	p, err := authenticate(ctx, tx, token)
	if err != nil {
		return empty, err
	}
	if p.Kind == BrowserCapability {
		return empty, failure("forbidden", "browser credentials are read-only")
	}
	actor := string(p.Kind) + ":" + p.SubjectID
	var r Receipt
	var oldDigest string
	err = tx.QueryRowContext(ctx, `SELECT digest,operation_id,resource_id,revision,outcome,body_ref,received_at FROM operation_receipts WHERE actor=? AND operation_id=?`, actor, op.ID).Scan(&oldDigest, &r.OperationID, &r.ResourceID, &r.Revision, &r.Outcome, &r.BodyRef, &r.ReceivedAt)
	if err == nil {
		// A different command may use another resource type. A digest mismatch
		// discloses no saved data and must not enter that command's typed replay.
		if oldDigest != digest {
			return empty, failure("operation_conflict", "operation ID already used with a different payload or scope")
		}
		if err = authorizeReplay(ctx, tx, p, r); err != nil {
			return empty, publicError(err)
		}
		return r, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, storageError()
	}
	r, err = apply(ctx, tx, p)
	if err != nil {
		return empty, publicError(err)
	}
	if len(r.ResourceID) == 0 || len(r.ResourceID) > 128 || r.Revision < 0 || len(r.Outcome) == 0 || len(r.Outcome) > 64 || len(r.BodyRef) > 128 {
		return empty, failure("invalid_input", "receipt must contain bounded references and a scalar outcome")
	}
	r.OperationID = op.ID
	r.ReceivedAt = time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO operation_receipts(actor,operation_id,name,scope,digest,resource_id,revision,outcome,body_ref,received_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, actor, op.ID, op.Name, op.Scope, digest, r.ResourceID, r.Revision, r.Outcome, r.BodyRef, r.ReceivedAt)
	if err != nil {
		return empty, storageError()
	}
	if tx.Commit() != nil {
		return empty, storageError()
	}
	if s.noticeWake != nil {
		s.noticeWake()
	}
	return r, nil
}

type StorageStatus struct {
	DatabaseID    string `json:"database_id"`
	DatabaseBytes int64  `json:"database_bytes"`
	WALBytes      int64  `json:"wal_bytes"`
	SHMBytes      int64  `json:"shm_bytes"`
}

func (s *Store) Status() (StorageStatus, error) {
	status := StorageStatus{DatabaseID: s.id}
	for suffix, target := range map[string]*int64{"": &status.DatabaseBytes, "-wal": &status.WALBytes, "-shm": &status.SHMBytes} {
		info, err := os.Stat(s.path + suffix)
		if os.IsNotExist(err) && suffix != "" {
			continue
		}
		if err != nil {
			return status, storageError()
		}
		*target = info.Size()
	}
	return status, nil
}

// Backup is SQLite VACUUM INTO, followed by integrity checking and fsync. The
// private destination must not exist. This maintenance operation changes no
// domain records; retry with an existing destination conflicts, never overwrites.
func (s *Store) Backup(ctx context.Context, token, destination string) (StorageStatus, error) {
	empty := StorageStatus{}
	if !filepath.IsAbs(destination) || filepath.Clean(destination) == s.path {
		return empty, failure("invalid_input", "backup destination must be a new absolute filename")
	}
	parent := filepath.Dir(destination)
	info, err := os.Lstat(parent)
	if err != nil {
		return empty, failure("invalid_input", "create a private backup directory first")
	}
	if err = checkPrivate(info, true); err != nil {
		return empty, err
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return empty, storageError()
	}
	resolvedState, err := filepath.EvalSymlinks(filepath.Dir(s.path))
	if err != nil {
		return empty, storageError()
	}
	relative, err := filepath.Rel(resolvedState, resolvedParent)
	if err != nil || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return empty, failure("invalid_input", "backups must be outside application state")
	}
	if _, err = os.Lstat(destination); err == nil {
		return empty, failure("conflict", "backup destination already exists")
	} else if !os.IsNotExist(err) {
		return empty, storageError()
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return empty, storageError()
	}
	defer conn.Close()
	p, err := authenticate(ctx, conn, token)
	if err != nil {
		return empty, err
	}
	if p.Kind != OperatorCapability {
		return empty, failure("forbidden", "Operator credential required")
	}
	f, err := os.CreateTemp(parent, ".a-stack-backup-*")
	if err != nil {
		return empty, storageError()
	}
	staging := f.Name()
	defer func() { f.Close(); os.Remove(staging) }()
	if _, err = conn.ExecContext(ctx, "VACUUM main INTO ?", staging); err != nil {
		return empty, storageError()
	}
	backup, err := sql.Open("sqlite3", (&url.URL{Scheme: "file", Path: staging}).String()+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		return empty, storageError()
	}
	var integrity string
	err = backup.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity)
	closeErr := backup.Close()
	if err != nil || closeErr != nil || integrity != "ok" || f.Sync() != nil {
		return empty, storageError()
	}
	// Publish only a complete, checked snapshot, and never clobber a prior one.
	if err = os.Link(staging, destination); errors.Is(err, os.ErrExist) {
		return empty, failure("conflict", "backup destination already exists")
	} else if err != nil {
		return empty, storageError()
	}
	if err = syncDir(parent); err != nil {
		return empty, err
	}
	info, err = f.Stat()
	if err != nil {
		return empty, storageError()
	}
	return StorageStatus{DatabaseID: s.id, DatabaseBytes: info.Size()}, nil
}

func validOperationID(id string) bool {
	if len(id) < 8 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
