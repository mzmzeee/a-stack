//go:build cgo

package stack

import (
	"fmt"
	"os"
	"testing"

	"github.com/mattn/go-sqlite3"
)

func TestDatabaseStartErrorClassification(t *testing.T) {
	for _, code := range []sqlite3.ErrNo{sqlite3.ErrCorrupt, sqlite3.ErrNotADB, sqlite3.ErrFull, sqlite3.ErrIoErr, sqlite3.ErrPerm, sqlite3.ErrCantOpen, sqlite3.ErrBusy} {
		t.Run(code.Error(), func(t *testing.T) {
			err := databaseStartError("fixture.db", fmt.Errorf("wrapped: %w", sqlite3.Error{Code: code}))
			permanent := code == sqlite3.ErrCorrupt || code == sqlite3.ErrNotADB
			if err.DatabaseUnusable() != permanent || err.Retryable == permanent {
				t.Fatalf("wrong classification: %+v", err)
			}
		})
	}

	if err := databaseStartError("fixture.db", os.ErrPermission); !err.Retryable || err.DatabaseUnusable() {
		t.Fatal(err)
	}
}
