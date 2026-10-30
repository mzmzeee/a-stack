package stack

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireUnusable(t *testing.T, err error, want ...string) {
	t.Helper()

	e, ok := err.(*Error)
	if !ok || e.Code != "storage_unavailable" || !e.DatabaseUnusable() || e.Retryable {
		t.Fatalf("want an unusable database error, got %v", err)
	}

	for _, w := range want {
		if !strings.Contains(e.Message, w) {
			t.Fatalf("message %q lacks %q", e.Message, w)
		}
	}
}

// TestStartRefusesADamagedDatabase repeats the review: random bytes in page 5
// of a closed database must stop the next start, before it says ready.
func TestStartRefusesADamagedDatabase(t *testing.T) {
	service, dir, _ := startTest(t)
	execTest(t, service.Store, "CREATE TABLE filler(value TEXT); WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<200) INSERT INTO filler SELECT printf('%0500d', i) FROM n")
	path := filepath.Join(dir, "state.db")

	if err := service.Close(ctx); err != nil {
		t.Fatal(err)
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}

	if info, _ := f.Stat(); info.Size() < 10*4096 {
		t.Fatalf("database has %d bytes; the test needs more pages", info.Size())
	}

	garbage := make([]byte, 4096)
	_, _ = rand.Read(garbage)
	if _, err = f.WriteAt(garbage, 4*4096); err != nil || f.Close() != nil {
		t.Fatal("cannot damage page 5", err)
	}

	_, err = Start(ctx, dir, "")
	requireUnusable(t, err, path, "quick_check", "restore a backup", "#backup-and-restore")
}

func TestStartRefusesANewerOrForeignSchema(t *testing.T) {
	for name, pragma := range map[string]string{
		"newer":   fmt.Sprintf("PRAGMA application_id=%d; PRAGMA user_version=%d", applicationID, schemaVersion+1),
		"foreign": "PRAGMA application_id=123; PRAGMA user_version=2",
	} {
		t.Run(name, func(t *testing.T) {
			dir := privateTestDir(t)
			path, err := ensureDatabaseFile(dir)
			if err != nil {
				t.Fatal(err)
			}

			db, err := sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}

			if _, err = db.Exec("CREATE TABLE later(value TEXT); " + pragma); err != nil || db.Close() != nil {
				t.Fatal(err)
			}

			_, err = Start(ctx, dir, "")
			if name == "newer" {
				requireUnusable(t, err, path, fmt.Sprintf("schema version %d", schemaVersion+1), fmt.Sprintf("schema version %d or older", schemaVersion), "a-stack update", "restore a backup")
			} else {
				requireUnusable(t, err, path, "not an a-stack database")
			}

			if entries, _ := filepath.Glob(path + ".pre-v*"); len(entries) != 0 {
				t.Fatal("a refused database got a migration backup", entries)
			}
		})
	}
}

// TestBackupBeforeMigration builds a schema 5 database, the schema of v0.1.2,
// and checks that each start that migrates it first writes a private copy.
func TestBackupBeforeMigration(t *testing.T) {
	dir := privateTestDir(t)
	path, err := ensureDatabaseFile(dir)
	if err != nil {
		t.Fatal(err)
	}

	makeV5 := func() {
		t.Helper()

		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}

		if path, err = ensureDatabaseFile(dir); err != nil {
			t.Fatal(err)
		}

		db, err := sql.Open("sqlite3", path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		db.SetMaxOpenConns(1)

		if _, err = db.Exec(migration1 + migration2 + migration3 + migration4 + migration5); err != nil {
			t.Fatal(err)
		}

		if _, err = db.Exec(fmt.Sprintf("INSERT INTO metadata VALUES('database_id','%s'); PRAGMA application_id=%d; PRAGMA user_version=5", testDatabaseID, applicationID)); err != nil {
			t.Fatal(err)
		}
	}
	check := func(name string) {
		t.Helper()

		info, err := os.Lstat(name)
		if err != nil || posixModes && info.Mode().Perm() != 0o600 {
			t.Fatalf("backup %s: %v %v", name, info, err)
		}

		db, err := sql.Open("sqlite3", name+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()

		var version int
		var id, integrity string
		if db.QueryRow("PRAGMA user_version").Scan(&version) != nil || version != 5 || db.QueryRow("SELECT value FROM metadata WHERE key='database_id'").Scan(&id) != nil || id != testDatabaseID || db.QueryRow("PRAGMA integrity_check").Scan(&integrity) != nil || integrity != "ok" {
			t.Fatalf("backup %s: version %d, id %s, %s", name, version, id, integrity)
		}
	}
	makeV5()

	service, err := Start(ctx, dir, "")
	if err != nil {
		t.Fatal(err)
	}

	if countTest(t, service.Store, "PRAGMA user_version") != schemaVersion {
		t.Fatal("not migrated")
	}

	if err = service.Close(ctx); err != nil {
		t.Fatal(err)
	}

	check(path + ".pre-v5")
	first, _ := os.ReadFile(path + ".pre-v5")

	// A start without a migration writes no copy; a second migration never
	// replaces the first copy.
	service, err = Start(ctx, dir, "")
	if err != nil {
		t.Fatal(err)
	}

	if err = service.Close(ctx); err != nil {
		t.Fatal(err)
	}

	if entries, _ := filepath.Glob(path + ".pre-v*"); len(entries) != 1 {
		t.Fatal("a start without a migration wrote a backup", entries)
	}

	makeV5()

	service, err = Start(ctx, dir, "")
	if err != nil {
		t.Fatal(err)
	}

	if err = service.Close(ctx); err != nil {
		t.Fatal(err)
	}

	check(path + ".pre-v5.2")

	if again, _ := os.ReadFile(path + ".pre-v5"); string(again) != string(first) {
		t.Fatal("the first backup was replaced")
	}

	// Without room for the copy, the start stops before the migration.
	makeV5()

	for n := 3; n <= 100; n++ {
		if err = os.WriteFile(fmt.Sprintf("%s.pre-v5.%d", path, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	_, err = Start(ctx, dir, "")
	requireCode(t, err, "storage_unavailable")

	if !strings.Contains(err.Error(), "state.db.pre-v5, the backup before the schema migration") {
		t.Fatal(err)
	}

	db, _ := sql.Open("sqlite3", path)
	defer db.Close()

	if version := 0; db.QueryRow("PRAGMA user_version").Scan(&version) != nil || version != 5 {
		t.Fatal("migrated without a backup", version)
	}
}

var testDatabaseID = opaqueID()

// schema7Database is a closed schema 7 state directory with one Task, after
// the statements in extra.
func schema7Database(t *testing.T, extra string) (path, state string) {
	t.Helper()
	f := tasksFixture(t)
	f.create(t, "Task")

	path, state = f.service.Store.path, f.service.lock.dir
	if err := f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}

	execFile(t, path, undoMigration9+undoMigration8+extra)

	return path, state
}

func execFile(t *testing.T, path, statements string) {
	t.Helper()

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = db.Exec(statements); err != nil {
		t.Fatal(err)
	}

	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
}

// copies lists the pre-v7 copies and temporary files next to path.
func copies(path string) []string {
	entries, _ := filepath.Glob(path + ".pre-v7*")
	for i, e := range entries {
		entries[i] = strings.TrimPrefix(e, path)
	}

	return entries
}

// TestMigrationCopyIsReusedAfterAFailedStart repeats the review: each start
// that a broken reference stopped wrote another full copy, up to 100. Now the
// copy of an unchanged database is reused, and a changed one gets a new copy.
func TestMigrationCopyIsReusedAfterAFailedStart(t *testing.T) {
	path, state := schema7Database(t, "INSERT INTO task_content VALUES('orphan','missing-task','report','x','missing-agent','now')")
	for range 3 {
		_, err := Start(ctx, state, "127.0.0.1:0")
		var e *Error
		if !errors.As(err, &e) || !e.DatabaseUnusable() || !strings.Contains(e.Message, "foreign_key_check") {
			t.Fatalf("start = %v", err)
		}
	}

	if got := copies(path); len(got) != 1 || got[0] != ".pre-v7" {
		t.Fatal("copies after three failed starts:", got)
	}

	execFile(t, path, "DELETE FROM task_content WHERE id='orphan'")

	service := serveTest(t, state, "127.0.0.1:0")
	if got := copies(path); len(got) != 2 || got[1] != ".pre-v7.2" {
		t.Fatal("a changed database got no new copy:", got)
	}

	for _, want := range []string{"Migrated the state database from schema 7 to 15 in ", "The copy from before is " + path + ".pre-v7.2; an older a-stack cannot open the database now.", "#go-back-to-an-older-version"} {
		if !strings.Contains(service.Store.Migrated, want) {
			t.Fatalf("migration line %q lacks %q", service.Store.Migrated, want)
		}
	}
}

// TestMigrationCopyIsWrittenAtomically repeats the review: a crash during the
// copy left the file under its final name, and the next open of it rolled it
// back to an empty file that passes the integrity check. Now the copy is
// written to state.db.pre-vN.tmp first, and only a copy of this database at
// this schema version is reused.
func TestMigrationCopyIsWrittenAtomically(t *testing.T) {
	path, state := schema7Database(t, "")
	source, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// What a crash left: a cut-off temporary copy with its journal, and an
	// empty final copy with the database's time, as after the rollback.
	for _, name := range []string{".pre-v7.tmp", ".pre-v7.tmp-journal", ".pre-v7"} {
		if err = os.WriteFile(path+name, nil, 0o600); err != nil || os.Chtimes(path+name, source.ModTime(), source.ModTime()) != nil {
			t.Fatal(err)
		}
	}

	serveTest(t, state, "127.0.0.1:0")

	if got := copies(path); len(got) != 2 || got[0] != ".pre-v7" || got[1] != ".pre-v7.2" {
		t.Fatal("copies:", got)
	}

	if info, err := os.Stat(path + ".pre-v7"); err != nil || info.Size() != 0 {
		t.Fatal("an earlier file was replaced", err)
	}

	db, err := sql.Open("sqlite3", path+".pre-v7.2?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var version int
	var integrity string
	if db.QueryRow("PRAGMA user_version").Scan(&version) != nil || version != 7 || db.QueryRow("PRAGMA integrity_check").Scan(&integrity) != nil || integrity != "ok" {
		t.Fatalf("copy: version %d, %s", version, integrity)
	}
}

// TestMigrationNeedsFreeSpace: from schema 7 the copy and the two rebuilds,
// schema 8 and 9 in one transaction, need up to four times the database size;
// without it, the start stops before it writes anything. Measured with a
// 94 MB v0.1.5 database: the state directory grew by 3.96 times its size.
func TestMigrationNeedsFreeSpace(t *testing.T) {
	path, _ := schema7Database(t, "")
	db, err := sql.Open(driverName, fileURI(path)+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	size := uint64(info.Size())
	free := func(n uint64) {
		s.disk.mu.Lock()
		defer s.disk.mu.Unlock()
		s.disk.free = func(string) (uint64, error) { return n, nil }
		s.disk.at = time.Time{}
	}
	free(4*size - 1)
	_, err = s.backupBeforeMigration(ctx, 7)
	requireCode(t, err, "storage_unavailable")

	if !strings.Contains(err.Error(), "the schema migration of state.db needs about") || !strings.Contains(err.Error(), "free some space and start a-stack again") || len(copies(path)) != 0 {
		t.Fatal(err, copies(path))
	}

	free(4 * size)

	if name, err := s.backupBeforeMigration(ctx, 7); err != nil || name != path+".pre-v7" {
		t.Fatal(name, err)
	}

	// Reusing the copy needs room only for the rebuilds.
	free(3 * size)

	if name, err := s.backupBeforeMigration(ctx, 7); err != nil || name != path+".pre-v7" || len(copies(path)) != 1 {
		t.Fatal(name, err, copies(path))
	}

	free(3*size - 1)

	if _, err = s.backupBeforeMigration(ctx, 7); err == nil {
		t.Fatal("a migration without room for the rebuilds")
	}

	// From schema 8, one rebuild: three times with the copy.
	free(3 * size)

	if s.migrationSpace(int64(size), 8, true) != nil || s.migrationSpace(int64(size)+1, 8, true) == nil {
		t.Fatal("schema 8 needs three times the database size")
	}
}
