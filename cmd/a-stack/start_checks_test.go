package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// A database with a newer schema stops serve with exit code 4, which the
// systemd unit lists in RestartPreventExitStatus, and prints no ready line.
func TestServeExitsFourForANewerSchema(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := stack.MkdirPrivate(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE later(value TEXT); PRAGMA application_id=1095980107; PRAGMA user_version=99"); err != nil || db.Close() != nil {
		t.Fatal(err)
	}
	_, stderr := commandResult(t, []string{"serve", "--state-dir", dir, "--json"}, 4)
	var envelope struct{ Error stack.Error }
	if json.Unmarshal(stderr, &envelope) != nil || envelope.Error.Code != "storage_unavailable" || envelope.Error.Retryable || !strings.Contains(envelope.Error.Message, "schema version 99") {
		t.Fatalf("stderr %s", stderr)
	}
	unit, err := os.ReadFile("../../contrib/systemd/a-stack.service")
	if err != nil || !strings.Contains(string(unit), "\nRestartPreventExitStatus=3 4\n") {
		t.Fatal("the systemd unit does not stop restarts for exit code 4", err)
	}
}
