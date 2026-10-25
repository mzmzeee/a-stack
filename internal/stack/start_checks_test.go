package stack

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		if err != nil || info.Mode().Perm() != 0600 {
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
		if err = os.WriteFile(fmt.Sprintf("%s.pre-v5.%d", path, n), nil, 0600); err != nil {
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
