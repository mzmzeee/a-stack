package stack

import (
	"path/filepath"
	"testing"
)

const undoMigration14 = undoMigration15 + `DROP TABLE owner_join_codes; DROP TABLE session_links; DROP TABLE session_locations; DROP TABLE session_transfers; DROP TABLE workspace_repositories; DROP TABLE session_invites; DROP TABLE session_operators; PRAGMA user_version=13;`

func TestSchema14Upgrade(t *testing.T) {
	f := tasksFixture(t)
	path := filepath.Join(f.service.lock.dir, "state.db")
	if err := f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	execFile(t, path, undoMigration14)
	s, err := openStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if countTest(t, s, "PRAGMA user_version") != schemaVersion {
		t.Fatal("schema not upgraded")
	}
	if _, err = s.InspectSession(ctx, f.operator.Token, f.ref(0)); err != nil {
		t.Fatal(err)
	}
}
