package stack

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
)

const undoMigration12 = undoMigration13 + `DROP TABLE notice_ack_refs; DROP TABLE mail_acks; PRAGMA user_version=11;`

func TestMigration12SQL(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(migration12); err != nil {
		t.Fatal(err)
	}
}

func TestMigration12FromPopulated11(t *testing.T) {
	f := mailFixture(t)
	f.send(t, f.a.Token, "retained @03-worker")
	path := f.service.Store.path
	if err := f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	execFile(t, path, undoMigration12)
	before := upgradeRows(t, path)
	s, err := openStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if countTest(t, s, "PRAGMA user_version") != schemaVersion || countTest(t, s, "PRAGMA foreign_keys") != 1 || countTest(t, s, "SELECT count(*) FROM pragma_foreign_key_check") != 0 {
		t.Fatal("schema/FK check")
	}
	if !strings.Contains(s.Migrated, "from schema 11 to 14") {
		t.Fatal(s.Migrated)
	}
	if !reflect.DeepEqual(before, upgradeRows(t, path)) || !reflect.DeepEqual(before, upgradeRows(t, path+".pre-v11")) {
		t.Fatal("retained rows changed")
	}
	if countTest(t, s, "SELECT count(*) FROM mail_acks") != 0 {
		t.Fatal("invented obligation")
	}
}

func TestMigration12Rollback(t *testing.T) {
	f := mailFixture(t)
	path := f.service.Store.path
	if err := f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	execFile(t, path, undoMigration12+"CREATE TABLE notice_ack_refs(collision);")
	if _, err := openStore(ctx, path); err == nil {
		t.Fatal("expected migration failure")
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v, n int
	if err = db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != 11 {
		t.Fatal(v, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='mail_acks'").Scan(&n); err != nil || n != 0 {
		t.Fatal("partial migration committed", n, err)
	}
}
