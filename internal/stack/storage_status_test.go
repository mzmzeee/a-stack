package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// storage status shows the free pages inside the database file, which a
// migration or deleted rows leave and only a backup and restore give back.
func TestStorageStatusShowsFreePagesInTheFile(t *testing.T) {
	s, err := Start(ctx, privateTestDir(t), "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	status, err := s.Store.Status()
	if err != nil || status.FreeInFileBytes*4 >= status.DatabaseBytes || status.Note != "" {
		t.Fatalf("a new database has many free pages: %v %+v", err, status)
	}
	if _, err = s.Store.db.Exec("CREATE TABLE filler(body BLOB); WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<512) INSERT INTO filler SELECT randomblob(4096) FROM n"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Store.db.Exec("DROP TABLE filler; PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	status, err = s.Store.Status()
	if err != nil || status.FreeInFileBytes < 512*4096 || status.FreeInFileBytes > status.DatabaseBytes || !strings.Contains(status.Note, "storage backup") {
		t.Fatalf("free pages after a drop: %v %+v", err, status)
	}
}

// The checkout guard names the folder that has the .git, so a user with a
// Git repository in the home folder sees why and what to do.
func TestStateInsideACheckoutNamesTheCheckout(t *testing.T) {
	root := privateTestDir(t)
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolvePath(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Start(ctx, filepath.Join(root, "state"), "")
	requireCode(t, err, "invalid_input")
	if message := err.(*Error).Message; !strings.Contains(message, resolved+" is a Git checkout") || !strings.Contains(message, "--state-dir") {
		t.Fatal(message)
	}
}
