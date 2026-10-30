//go:build !cgo

package stack

import (
	"database/sql"

	"github.com/mattn/go-sqlite3"
)

// Without cgo, go-sqlite3 is a stub that cannot open a database. These
// stand-ins let go vet check this package, and serve say why it cannot
// start; see sqlite_cgo.go.

const builtWithCgo = false

func init() { sql.Register(driverName, &sqlite3.SQLiteDriver{}) }

func sqliteClass(error) string { return "" }

func sqliteCorrupt(error) bool { return false }
