package stack

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
)

const undoMigration15 = `DROP INDEX notice_batch_refs_message; ALTER TABLE agents DROP COLUMN last_mail_read_at; PRAGMA user_version=14;`

func TestMigration15PreservesPopulatedRows(t *testing.T) {
	f := mailFixture(t)
	f.send(t, f.a.Token, "retained @03-worker")
	path := f.service.Store.path
	if err := f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	execFile(t, path, undoMigration15)
	before := upgradeRows(t, path)
	s, err := openStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if countTest(t, s, "PRAGMA user_version") != 15 || countTest(t, s, "SELECT count(*) FROM pragma_foreign_key_check") != 0 {
		t.Fatal("schema/FK check")
	}
	if !strings.Contains(s.Migrated, "from schema 14 to 15") {
		t.Fatal(s.Migrated)
	}
	if !reflect.DeepEqual(before, upgradeRows(t, path)) || !reflect.DeepEqual(before, upgradeRows(t, path+".pre-v14")) {
		t.Fatal("retained rows changed")
	}
	if countTest(t, s, "SELECT count(*) FROM agents WHERE last_mail_read_at IS NOT NULL") != 0 {
		t.Fatal("invented read")
	}
}

func TestMigration15Rollback(t *testing.T) {
	f := mailFixture(t)
	path := f.service.Store.path
	if err := f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	execFile(t, path, "ALTER TABLE agents DROP COLUMN last_mail_read_at; PRAGMA user_version=14;") // duplicate index forces failure after the column
	if _, err := openStore(ctx, path); err == nil {
		t.Fatal("expected migration failure")
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	if err = db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != 14 {
		t.Fatal(v, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM pragma_table_info('agents') WHERE name='last_mail_read_at'").Scan(&v); err != nil || v != 0 {
		t.Fatal("partial column committed", v, err)
	}
}
