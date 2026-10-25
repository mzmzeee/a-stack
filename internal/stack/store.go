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
	"sync"
	"time"
)

const schemaVersion = 8
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
	budget        writeBudget
	disk          diskSpace
	marksMu       sync.Mutex
	marks         map[string]archiveMarkState // by Session ID
}

// stmtCacheSize bounds the prepared statements the driver keeps. Past it, the
// driver finalizes the least recently used one. Multi-statement scripts, such
// as the migrations, are never cached.
const stmtCacheSize = 128

// JournalSizeLimit is the size that SQLite cuts the WAL file back to when it
// starts the WAL again after a checkpoint. A burst, such as a migration or a
// mail flood, can grow the WAL far past the about 4 MB that the default
// automatic checkpoint (every 1,000 pages) leaves; without a limit the file
// keeps that size until the service stops. Twice the normal size means that
// ordinary use never cuts and grows the file again.
const JournalSizeLimit = 8 << 20

// driverName is go-sqlite3 with JournalSizeLimit set on each new connection;
// the driver has no DSN parameter for it. sqlite_cgo.go registers it.
const driverName = "sqlite3-a-stack"

func openStore(ctx context.Context, path string) (*Store, error) {
	u := url.URL{Scheme: "file", Path: path}
	// The driver keeps up to stmtCacheSize single-statement queries prepared on
	// the one connection, so hot queries are not compiled again on every call.
	db, err := sql.Open(driverName, u.String()+"?_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on&_busy_timeout=5000&_txlock=immediate&_stmt_cache_size="+strconv.Itoa(stmtCacheSize))
	if err != nil {
		return nil, storageError(err)
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
		return nil, storageError(err)
	}
	if !sqliteSupported(s.sqliteVersion) {
		return nil, failure("storage_unavailable", "SQLite >=3.51.3 is required")
	}
	// A damaged database must stop the start here, before it says ready.
	var check string
	if err = db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		return nil, unusableDatabase("the state database %s failed the integrity check (PRAGMA quick_check); stop here, keep the file, and restore a backup: %s", path, restoreDocs)
	}
	var version, app int
	if db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version) != nil || db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&app) != nil {
		return nil, storageError()
	}
	if version > 0 && version < schemaVersion && app == applicationID {
		if err = s.backupBeforeMigration(ctx, version); err != nil {
			return nil, err
		}
	}
	if err = s.migrate(ctx, migration1); err != nil {
		return nil, err
	}
	// Close the connection that ran the migrations, and with it every statement
	// it cached against the old schema. The pool opens a fresh one on demand.
	db.SetMaxIdleConns(0)
	db.SetMaxIdleConns(1)
	if err = s.recoverNotices(ctx); err != nil {
		return nil, err
	}
	if err = db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='database_id'").Scan(&s.id); err != nil || !validToken(s.id) {
		return nil, storageError()
	}
	ok = true
	return s, nil
}

const restoreDocs = "https://github.com/mzmzeee/a-stack/blob/main/docs/foundation.md#backup-and-restore"

// backupBeforeMigration copies the database at schema version to
// state.db.pre-vN with VACUUM INTO before a migration changes it. It never
// replaces an earlier copy: the next one is state.db.pre-vN.2, and so on.
func (s *Store) backupBeforeMigration(ctx context.Context, version int) error {
	base := s.path + ".pre-v" + strconv.Itoa(version)
	for n := 1; n <= 100; n++ {
		name := base
		if n > 1 {
			name += "." + strconv.Itoa(n)
		}
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			break
		}
		// VACUUM INTO accepts the empty file, which keeps the owner-only mode.
		_, err = s.db.ExecContext(ctx, "VACUUM main INTO ?", name)
		if err == nil {
			err = f.Sync()
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err == nil && syncDir(filepath.Dir(s.path)) == nil {
			return nil
		}
		os.Remove(name)
		break
	}
	return failure("storage_unavailable", fmt.Sprintf("cannot write %s.pre-v%d, the backup before the schema migration; check free space and permissions in the state directory", filepath.Base(s.path), version))
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

// migration8 keeps Task texts and Task mail in rowid tables. A WITHOUT ROWID
// table stores a row of more than about 1 KB on its own 4 KB overflow page; a
// rowid table keeps rows of up to about 4 KB in the table page. The foreign key
// of a mention now uses the primary key of its message, and a trigger checks
// that the mention names the message's Task, which the composite foreign key
// did before. So mail_messages needs no UNIQUE(id,task_id) index any more.
// task_content_task keeps only task_id, which is all the foreign key check of
// a Task needs: every read finds a text by its ID.
const migration8 = `
CREATE TABLE task_content_v8(id TEXT PRIMARY KEY NOT NULL,task_id TEXT NOT NULL REFERENCES tasks(id),kind TEXT NOT NULL,body TEXT NOT NULL,actor_id TEXT NOT NULL REFERENCES agents(id),received_at TEXT NOT NULL);
INSERT INTO task_content_v8 SELECT id,task_id,kind,body,actor_id,received_at FROM task_content ORDER BY task_id,received_at;
DROP TABLE task_content;
ALTER TABLE task_content_v8 RENAME TO task_content;
CREATE INDEX task_content_task ON task_content(task_id);
CREATE TABLE mail_messages_v8(
 id TEXT PRIMARY KEY NOT NULL, task_id TEXT NOT NULL REFERENCES tasks(id), sequence INTEGER NOT NULL CHECK(sequence>0),
 sender_id TEXT NOT NULL REFERENCES agents(id), received_at TEXT NOT NULL, body TEXT NOT NULL,
 UNIQUE(task_id,sequence)
);
INSERT INTO mail_messages_v8 SELECT id,task_id,sequence,sender_id,received_at,body FROM mail_messages ORDER BY task_id,sequence;
CREATE TABLE mail_mentions_v8(
 message_id TEXT NOT NULL REFERENCES mail_messages(id), task_id TEXT NOT NULL, recipient_id TEXT NOT NULL REFERENCES agents(id),
 notice_state TEXT NOT NULL CHECK(notice_state IN ('pending','submitting','submitted','uncertain','suppressed','self')),
 state_at TEXT NOT NULL,
 PRIMARY KEY(message_id,recipient_id)
) WITHOUT ROWID;
INSERT INTO mail_mentions_v8 SELECT message_id,task_id,recipient_id,notice_state,state_at FROM mail_mentions;
DROP TABLE mail_mentions;
DROP TABLE mail_messages;
ALTER TABLE mail_messages_v8 RENAME TO mail_messages;
ALTER TABLE mail_mentions_v8 RENAME TO mail_mentions;
CREATE INDEX mail_mentions_pending ON mail_mentions(notice_state,recipient_id,message_id);
CREATE INDEX mail_mentions_membership ON mail_mentions(task_id,recipient_id,notice_state,message_id);
CREATE TRIGGER mail_mentions_task BEFORE INSERT ON mail_mentions
 WHEN NEW.task_id IS NOT (SELECT task_id FROM mail_messages WHERE id=NEW.message_id)
 BEGIN SELECT RAISE(ABORT,'FOREIGN KEY constraint failed'); END;
CREATE TRIGGER mail_mentions_task_change BEFORE UPDATE OF message_id,task_id ON mail_mentions
 WHEN NEW.task_id IS NOT (SELECT task_id FROM mail_messages WHERE id=NEW.message_id)
 BEGIN SELECT RAISE(ABORT,'FOREIGN KEY constraint failed'); END;
`

// migrate runs every migration the database lacks in one transaction.
// Schema 8 rebuilds tables that other tables reference, which SQLite allows
// only with foreign keys off, and it cannot change that inside a transaction.
// So the migrations run on one connection with foreign keys off, and
// PRAGMA foreign_key_check must find no broken reference before the commit.
func (s *Store) migrate(ctx context.Context, migration string) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return storageError(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return storageError(err)
	}
	defer conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback()
	var version, app int
	if tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version) != nil || tx.QueryRowContext(ctx, "PRAGMA application_id").Scan(&app) != nil {
		return storageError()
	}
	if version == schemaVersion && app == applicationID {
		return nil
	}
	if version > schemaVersion && app == applicationID {
		return unusableDatabase("the state database %s has schema version %d, but this a-stack supports schema version %d or older; update a-stack with a-stack update, or restore a backup made with this version: %s", s.path, version, schemaVersion, restoreDocs)
	}
	if version > schemaVersion || version < 0 || (version > 0 && app != applicationID) || (version == 0 && app != 0) {
		return unusableDatabase("the state database %s is not an a-stack database or has an unknown schema (version %d); use another state directory, or restore a backup: %s", s.path, version, restoreDocs)
	}
	if version == 0 {
		var count int
		if tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name NOT GLOB 'sqlite_*'").Scan(&count) != nil {
			return storageError()
		}
		if count != 0 {
			return unusableDatabase("the state database %s is not an a-stack database or has an unknown schema (version 0); use another state directory, or restore a backup: %s", s.path, restoreDocs)
		}
		if _, err = tx.ExecContext(ctx, migration); err != nil {
			return storageError(err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES('database_id',?)", opaqueID()); err != nil {
			return storageError(err)
		}
		if _, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=1; PRAGMA application_id=%d", applicationID)); err != nil {
			return storageError(err)
		}
		version = 1
	}
	if version < 2 {
		if _, err = tx.ExecContext(ctx, migration2); err != nil {
			return storageError(err)
		}
		if _, err = tx.ExecContext(ctx, "PRAGMA user_version=2"); err != nil {
			return storageError(err)
		}
	}
	if version < 3 {
		if _, err = tx.ExecContext(ctx, migration3); err != nil {
			return storageError(err)
		}
		if _, err = tx.ExecContext(ctx, "PRAGMA user_version=3"); err != nil {
			return storageError(err)
		}
	}
	if version < 4 {
		if _, err = tx.ExecContext(ctx, migration4); err != nil {
			return storageError(err)
		}
		if _, err = tx.ExecContext(ctx, "PRAGMA user_version=4"); err != nil {
			return storageError(err)
		}
	}
	if version < 5 {
		if _, err = tx.ExecContext(ctx, migration5); err != nil {
			return storageError(err)
		}
		if _, err = tx.ExecContext(ctx, "PRAGMA user_version=5"); err != nil {
			return storageError(err)
		}
	}
	if version < 6 {
		if _, err = tx.ExecContext(ctx, migration6); err != nil {
			return storageError(err)
		}
		if _, err = tx.ExecContext(ctx, "PRAGMA user_version=6"); err != nil {
			return storageError(err)
		}
	}
	if version < 7 {
		if _, err = tx.ExecContext(ctx, migration7); err != nil {
			return storageError(err)
		}
		if _, err = tx.ExecContext(ctx, "PRAGMA user_version=7"); err != nil {
			return storageError(err)
		}
	}
	if version < 8 {
		if _, err = tx.ExecContext(ctx, migration8); err != nil {
			return storageError(err)
		}
		if _, err = tx.ExecContext(ctx, "PRAGMA user_version=8"); err != nil {
			return storageError(err)
		}
	}
	var broken bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pragma_foreign_key_check)").Scan(&broken); err != nil {
		return storageError(err)
	}
	if broken {
		return unusableDatabase("the state database %s has broken references (PRAGMA foreign_key_check), so it was not migrated; restore a backup: %s", s.path, restoreDocs)
	}
	if err = tx.Commit(); err != nil {
		return storageError(err)
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

// issueViewToken stores the hash of a new view token for one Session in the
// caller's transaction and returns the token. Operator and Agent credentials
// are not issued here: the CLI makes them and the service stores their hash.
func issueViewToken(ctx context.Context, tx *sql.Tx, session string) (string, error) {
	token := opaqueID()
	_, err := tx.ExecContext(ctx, "INSERT INTO capabilities(token_hash,kind,subject_id,session_id,expires_at) VALUES(?,'browser',?,?,?)", tokenHash(token), opaqueID(), session, clockNow().Add(ViewTokenAge).Unix())
	if err != nil {
		return "", storageError(err)
	}
	return token, nil
}
func authenticate(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, token string) (Principal, error) {
	var p Principal
	if !validToken(token) {
		return p, failure("unauthorized", "valid bearer credential required")
	}
	var expires int64
	err := q.QueryRowContext(ctx, "SELECT kind,subject_id,session_id,expires_at FROM capabilities WHERE token_hash=?", tokenHash(token)).Scan(&p.Kind, &p.SubjectID, &p.SessionID, &expires)
	if errors.Is(err, sql.ErrNoRows) || err == nil && expires != 0 && expires <= clockNow().Unix() {
		return p, failure("unauthorized", "valid bearer credential required")
	}
	if err != nil {
		return p, storageError(err)
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
			return storageError(err)
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
	if !validOperationID(op.ID) {
		return empty, failure("invalid_input", OperationIDRule)
	}
	if len(op.Name) == 0 || len(op.Name) > 64 || len(op.Scope) == 0 || len(op.Scope) > 128 || len(op.Payload) > MaxRequestBytes || !json.Valid(op.Payload) || authorizeReplay == nil || apply == nil {
		return empty, failure("invalid_input", "bounded payload, scope and authorization callbacks required")
	}
	b, _ := json.Marshal(struct {
		Name, Scope string
		Payload     json.RawMessage
	}{op.Name, op.Scope, op.Payload})
	sum := sha256.Sum256(b)
	digest := hex.EncodeToString(sum[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, storageError(err)
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
			return empty, operationError(op.Name, err)
		}
		return r, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, operationError(op.Name, err)
	}
	// A replay above is never limited: it stores nothing new.
	if p.Kind == AgentCapability {
		if err = s.budget.check(p.SubjectID, len(op.Payload)); err != nil {
			return empty, err
		}
	}
	r, err = apply(ctx, tx, p)
	if err != nil {
		return empty, operationError(op.Name, err)
	}
	if len(r.ResourceID) == 0 || len(r.ResourceID) > 128 || r.Revision < 0 || len(r.Outcome) == 0 || len(r.Outcome) > 64 || len(r.BodyRef) > 128 {
		return empty, failure("invalid_input", "receipt must contain bounded references and a scalar outcome")
	}
	r.OperationID = op.ID
	r.ReceivedAt = time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO operation_receipts(actor,operation_id,name,scope,digest,resource_id,revision,outcome,body_ref,received_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, actor, op.ID, op.Name, op.Scope, digest, r.ResourceID, r.Revision, r.Outcome, r.BodyRef, r.ReceivedAt)
	if err != nil {
		return empty, operationError(op.Name, err)
	}
	if err = tx.Commit(); err != nil {
		return empty, operationError(op.Name, err)
	}
	if p.Kind == AgentCapability {
		s.budget.charge(p.SubjectID, len(op.Payload))
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
	// FreeBytes is the free space of the database's file system, measured at
	// most diskCheckInterval ago. It is absent when it cannot be measured.
	FreeBytes *uint64 `json:"free_bytes,omitempty"`
	LowDisk   bool    `json:"low_disk,omitempty"`
	Warning   string  `json:"warning,omitempty"`
}

func (s *Store) Status() (StorageStatus, error) {
	status := StorageStatus{DatabaseID: s.id}
	for suffix, target := range map[string]*int64{"": &status.DatabaseBytes, "-wal": &status.WALBytes, "-shm": &status.SHMBytes} {
		info, err := os.Stat(s.path + suffix)
		if os.IsNotExist(err) && suffix != "" {
			continue
		}
		if err != nil {
			return status, storageError(err)
		}
		*target = info.Size()
	}
	if free, err := s.disk.get(s.path); err == nil {
		status.FreeBytes = &free
		if free < LowDiskBytes {
			status.LowDisk = true
			status.Warning = "less than 1 GiB of disk space is free for the a-stack state; free some space, because a-stack never deletes retained data"
		}
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
		return empty, storageError(err)
	}
	resolvedState, err := filepath.EvalSymlinks(filepath.Dir(s.path))
	if err != nil {
		return empty, storageError(err)
	}
	relative, err := filepath.Rel(resolvedState, resolvedParent)
	if err != nil || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return empty, failure("invalid_input", "backups must be outside application state")
	}
	if _, err = os.Lstat(destination); err == nil {
		return empty, failure("conflict", "backup destination already exists")
	} else if !os.IsNotExist(err) {
		return empty, storageError(err)
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return empty, storageError(err)
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
		return empty, storageError(err)
	}
	staging := f.Name()
	defer func() { f.Close(); os.Remove(staging) }()
	if _, err = conn.ExecContext(ctx, "VACUUM main INTO ?", staging); err != nil {
		return empty, storageError(err)
	}
	backup, err := sql.Open("sqlite3", (&url.URL{Scheme: "file", Path: staging}).String()+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		return empty, storageError(err)
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
		return empty, storageError(err)
	}
	if err = syncDir(parent); err != nil {
		return empty, err
	}
	info, err = f.Stat()
	if err != nil {
		return empty, storageError(err)
	}
	return StorageStatus{DatabaseID: s.id, DatabaseBytes: info.Size()}, nil
}

// OperationIDRule is the error message for an operation ID that breaks it.
const OperationIDRule = "operation ID must be 8–128 letters, digits, _ or -"

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
