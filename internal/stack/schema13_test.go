package stack

import (
	"database/sql"
	"testing"
)

const undoMigration13 = `DROP INDEX mail_messages_urgent; ALTER TABLE mail_messages DROP COLUMN priority; PRAGMA user_version=12;`

func TestMigration13RetainsMailAndDefaultsPriority(t *testing.T) {
	f := mailFixture(t)
	f.send(t, f.a.Token, "Retained mail")
	path := f.service.Store.path
	if err := f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	execFile(t, path, undoMigration13)
	s, err := openStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if countTest(t, s, "PRAGMA user_version") != schemaVersion || countTest(t, s, "SELECT count(*) FROM mail_messages WHERE priority='normal' AND body='Retained mail'") != 1 || countTest(t, s, "SELECT count(*) FROM pragma_foreign_key_check") != 0 {
		t.Fatal("migration lost mail or default")
	}
	if _, err = s.db.Exec("UPDATE mail_messages SET priority='critical'"); err == nil {
		t.Fatal("invalid priority stored")
	}
}

func TestMigration13Rollback(t *testing.T) {
	f := mailFixture(t)
	path := f.service.Store.path
	if err := f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	execFile(t, path, undoMigration13+"CREATE INDEX mail_messages_urgent ON mail_messages(body);")
	if _, err := openStore(ctx, path); err == nil {
		t.Fatal("expected index collision")
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, columns int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 12 {
		t.Fatal(version, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM pragma_table_info('mail_messages') WHERE name='priority'").Scan(&columns); err != nil || columns != 0 {
		t.Fatal("partial migration", columns, err)
	}
}
