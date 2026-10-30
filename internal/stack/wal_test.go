package stack

import (
	"os"
	"testing"
)

// After a burst, the WAL file shrinks back to JournalSizeLimit once SQLite
// starts it again, also on a connection the pool opened later.
func TestWALShrinksAfterBurst(t *testing.T) {
	service, _, _ := startTest(t)
	s := service.Store
	if countTest(t, s, "PRAGMA journal_size_limit") != JournalSizeLimit {
		t.Fatal("journal_size_limit not set")
	}

	// A burst of 24 MiB in one transaction, more than the automatic checkpoint
	// can keep small.
	execTest(t, s, "CREATE TABLE test_burst(data BLOB)")
	execTest(t, s, "WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<384) INSERT INTO test_burst SELECT zeroblob(65536) FROM n")

	wal := func() int64 {
		info, err := os.Stat(s.path + "-wal")
		if err != nil {
			t.Fatal(err)
		}

		return info.Size()
	}
	if wal() < 3*JournalSizeLimit {
		t.Fatalf("burst WAL is only %d bytes", wal())
	}

	execTest(t, s, "PRAGMA wal_checkpoint(PASSIVE)")
	execTest(t, s, "INSERT INTO test_burst VALUES(x'00')") // starts the WAL again

	if size := wal(); size > JournalSizeLimit {
		t.Fatalf("WAL stays at %d bytes after the burst", size)
	}

	// A new connection gets the limit too.
	s.db.SetMaxIdleConns(0)
	s.db.SetMaxIdleConns(1)

	if countTest(t, s, "PRAGMA journal_size_limit") != JournalSizeLimit {
		t.Fatal("journal_size_limit lost on a new connection")
	}
}
