package stack

import (
	"database/sql"
	"testing"
)

func TestSchema16UnattachedMachineMetadata(t *testing.T) {
	f := noticeTestFixture(t)
	_, err := f.service.Store.RegisterAgent(ctx, f.operator.Token, AgentRegisterRequest{Context: f.ref(0), OperationID: opaqueID(), Name: "unattached-laptop", Machine: "laptop-A", CredentialHash: tokenHash(opaqueID())})
	if err != nil {
		t.Fatal(err)
	}

	path := f.service.Store.path
	if err = f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}

	execFile(t, path, undoMigration16)

	s, err := openStore(ctx, path)
	if err != nil {
		t.Fatalf("valid schema-15 unbound machine metadata blocks upgrade: %v", err)
	}
	defer s.db.Close()
}

func TestSchema16CollisionRollbackAndBackup(t *testing.T) {
	f := noticeTestFixture(t)

	path := f.service.Store.path
	id := f.service.Store.id
	if err := f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}

	execFile(t, path, undoMigration16)
	// Runtime IDs stay unique, but rebuild must refuse colliding concrete targets.
	execFile(t, path, `UPDATE agents SET runtime_id='reviewer-other',binding=(SELECT binding FROM agents WHERE runtime_id IS NOT NULL LIMIT 1) WHERE id=(SELECT id FROM agents WHERE runtime_id IS NULL LIMIT 1); DELETE FROM runtime_claims;`)

	s, err := openStore(ctx, path)
	if err == nil {
		s.db.Close()
		t.Fatal("colliding concrete bindings accepted")
	}

	db, e := sql.Open("sqlite3", path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()

	var version, columns int
	if e = db.QueryRow("PRAGMA user_version").Scan(&version); e != nil {
		t.Fatal(e)
	}

	if e = db.QueryRow("SELECT count(*) FROM pragma_table_info('agents') WHERE name='pane_not_before'").Scan(&columns); e != nil {
		t.Fatal(e)
	}

	if version != 15 || columns != 0 {
		t.Fatalf("migration partially applied: version=%d columns=%d", version, columns)
	}

	if !copyOf(ctx, path+".pre-v15", 15, id) {
		t.Fatal("missing valid pre-migration backup")
	}
}
