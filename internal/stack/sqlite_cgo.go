//go:build cgo

package stack

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/mattn/go-sqlite3"
)

// The parts of go-sqlite3 that exist only with cgo. a-stack always builds
// with cgo; sqlite_nocgo.go lets go vet check this package for another
// system on Linux, where cgo cannot cross-compile.

func init() {
	sql.Register(driverName, &sqlite3.SQLiteDriver{ConnectHook: func(c *sqlite3.SQLiteConn) error {
		_, err := c.Exec("PRAGMA journal_size_limit="+strconv.Itoa(JournalSizeLimit), nil)
		return err
	}})
}

// sqliteClass describes a driver error by its codes, or is "" for another error.
func sqliteClass(err error) string {
	var driver sqlite3.Error
	if !errors.As(err, &driver) {
		return ""
	}
	class := fmt.Sprintf("SQLite error %d (%s), extended code %d", int(driver.Code), driver.Code.Error(), int(driver.ExtendedCode))
	if driver.SystemErrno != 0 {
		class += fmt.Sprintf(", errno %d (%s)", int(driver.SystemErrno), driver.SystemErrno.Error())
	}
	return class
}
