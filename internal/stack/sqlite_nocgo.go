//go:build !cgo

package stack

import (
	"database/sql"

	"github.com/mattn/go-sqlite3"
)

// Without cgo, go-sqlite3 is a stub that cannot open a database. These
// stand-ins only let go vet check this package; see sqlite_cgo.go.

func init() { sql.Register(driverName, &sqlite3.SQLiteDriver{}) }

func sqliteClass(error) string { return "" }
