//go:build !cgo

package stack

// Without cgo, SQLite does not run at all; see sqlite_full_cgo_test.go.
func isSQLiteFull(error) bool { return false }
