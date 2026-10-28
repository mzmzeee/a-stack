package stack

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const undoMigration10 = undoMigration11 + `DROP TABLE task_claims; DROP TABLE expired_operations; DROP INDEX operation_receipts_age; PRAGMA user_version=9;`

// v017.db was written by the v0.1.7 tag, not by undoing the current schema.
func TestMigration10FromV017(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	data, err := os.ReadFile("testdata/v017.db")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	before := upgradeRows(t, path)
	s, err := openStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if countTest(t, s, "PRAGMA user_version") != schemaVersion || countTest(t, s, "PRAGMA foreign_keys") != 1 {
		t.Fatal("schema or foreign keys")
	}
	if !strings.Contains(s.Migrated, "from schema 9 to 12") {
		t.Fatal(s.Migrated)
	}
	if after := upgradeRows(t, path); !reflect.DeepEqual(before, after) {
		t.Fatal("retained rows changed")
	}
	backup := path + ".pre-v9"
	if !reflect.DeepEqual(before, upgradeRows(t, backup)) {
		t.Fatal("backup changed rows")
	}
	db, err := sql.Open("sqlite3", fileURI(backup)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if db.QueryRow("PRAGMA user_version").Scan(&version) != nil || version != 9 {
		t.Fatal("backup not schema 9")
	}
	if countTest(t, s, "SELECT count(*) FROM task_claims") != 0 || countTest(t, s, "SELECT count(*) FROM expired_operations") != 0 {
		t.Fatal("unexpected claims/expiry")
	}
}

func upgradeRows(t *testing.T, path string) map[string][]string {
	t.Helper()
	db, err := sql.Open("sqlite3", fileURI(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	result := map[string][]string{}
	for _, table := range []string{"metadata", "capabilities", "workspaces", "sessions", "agents", "tasks", "task_content", "task_questions", "task_history", "task_assignees", "workspace_orders", "mail_messages", "mail_mentions", "operation_receipts"} {
		rows, err := db.Query("SELECT * FROM " + table)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]sql.NullString, len(columns))
			args := make([]any, len(columns))
			for i := range values {
				args[i] = &values[i]
			}
			if err = rows.Scan(args...); err != nil {
				t.Fatal(err)
			}
			var row []string
			for i, v := range values {
				if columns[i] == "actor_login" || columns[i] == "actor_name" {
					continue
				}
				if v.Valid {
					row = append(row, v.String)
				} else {
					row = append(row, "<NULL>")
				}
			}
			result[table] = append(result[table], strings.Join(row, "\x00"))
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return result
}

func TestMigration10FreeSpaceAndRollback(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	path := s.path
	if err := f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	execFile(t, path, undoMigration10+"CREATE TABLE task_claims(sentinel TEXT); INSERT INTO task_claims VALUES('keep')")
	if _, err := openStore(ctx, path); err == nil {
		t.Fatal("migration should fail")
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if db.QueryRow("PRAGMA user_version").Scan(&version) != nil || version != 9 {
		t.Fatal("version changed")
	}
	var value string
	if db.QueryRow("SELECT sentinel FROM task_claims").Scan(&value) != nil || value != "keep" {
		t.Fatal("rollback lost sentinel")
	}
	s.disk.free = func(string) (uint64, error) { return 0, nil }
	if err := s.migrationSpace(1024, 9, true); err == nil {
		t.Fatal("low space allowed")
	}
}
