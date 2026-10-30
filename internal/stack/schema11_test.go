package stack

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const receiptActorStandIn = `ALTER TABLE operation_receipts ADD COLUMN actor_login TEXT; ALTER TABLE operation_receipts ADD COLUMN actor_name TEXT;`
const dropReceiptActor = `ALTER TABLE operation_receipts DROP COLUMN actor_login; ALTER TABLE operation_receipts DROP COLUMN actor_name;`
const taskActorStandIn = `ALTER TABLE task_history ADD COLUMN actor_login TEXT; ALTER TABLE task_history ADD COLUMN actor_name TEXT;`
const dropTaskActor = `ALTER TABLE task_history DROP COLUMN actor_login; ALTER TABLE task_history DROP COLUMN actor_name;`

const undoMigration11 = undoMigration12 + `
ALTER TABLE lifecycle_history DROP COLUMN actor_login;
ALTER TABLE lifecycle_history DROP COLUMN actor_name;
ALTER TABLE task_history DROP COLUMN actor_login;
ALTER TABLE task_history DROP COLUMN actor_name;
ALTER TABLE task_questions DROP COLUMN actor_login;
ALTER TABLE task_questions DROP COLUMN actor_name;
ALTER TABLE mail_messages DROP COLUMN actor_login;
ALTER TABLE mail_messages DROP COLUMN actor_name;
ALTER TABLE operation_receipts DROP COLUMN actor_login;
ALTER TABLE operation_receipts DROP COLUMN actor_name;
PRAGMA user_version=10;
`

func TestMigration11Rollback(t *testing.T) {
	f := tasksFixture(t)
	path := f.service.Store.path
	if err := f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	execFile(t, path, undoMigration11+"ALTER TABLE task_history ADD COLUMN actor_name TEXT;")
	if _, err := openStore(ctx, path); err == nil {
		t.Fatal("expected migration failure")
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, columns int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 10 {
		t.Fatal(version, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM pragma_table_info('lifecycle_history') WHERE name='actor_login'").Scan(&columns); err != nil || columns != 0 {
		t.Fatal("partial migration committed", columns, err)
	}
}

func TestMigration11FromSchema10(t *testing.T) {
	f := tasksFixture(t)
	id, q := askTest(t, f, "Keep the answer?")
	_, err := f.service.Store.AnswerQuestion(ctx, f.operator.Token, AnswerRequest{Context: f.ref(0), TaskID: id, QuestionID: q, OperationID: opaqueID(), Answer: "Yes"})
	if err != nil {
		t.Fatal(err)
	}
	path := f.service.Store.path
	if err = f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	execFile(t, path, undoMigration11)
	before := upgradeRows(t, path)
	s, err := openStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if countTest(t, s, "PRAGMA user_version") != schemaVersion || countTest(t, s, "PRAGMA foreign_keys") != 1 || countTest(t, s, "SELECT count(*) FROM pragma_foreign_key_check") != 0 {
		t.Fatal("schema/FK check")
	}
	if !strings.Contains(s.Migrated, "from schema 10 to 14") {
		t.Fatal(s.Migrated)
	}
	if !reflect.DeepEqual(before, upgradeRows(t, path)) || !reflect.DeepEqual(before, upgradeRows(t, path+".pre-v10")) {
		t.Fatal("migration changed retained rows")
	}
	db, err := sql.Open("sqlite3", fileURI(filepath.Clean(path+".pre-v10"))+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 10 {
		t.Fatal(version, err)
	}
	if countTest(t, s, "SELECT count(*) FROM task_questions WHERE actor_login IS NOT NULL") != 0 {
		t.Fatal("invented historical actor")
	}
}
