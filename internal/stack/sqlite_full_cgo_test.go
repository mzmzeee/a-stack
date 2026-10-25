//go:build cgo

package stack

import (
	"errors"

	"github.com/mattn/go-sqlite3"
)

// isSQLiteFull reports a real SQLITE_FULL from the driver. sqlite3.Error
// exists only with cgo; sqlite_full_nocgo_test.go lets go vet check this
// package for macOS on Linux, where cgo cannot cross-compile.
func isSQLiteFull(err error) bool {
	var e sqlite3.Error
	return errors.As(err, &e) && e.Code == sqlite3.ErrFull
}
