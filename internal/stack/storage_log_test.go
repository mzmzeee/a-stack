package stack

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"testing"
	"time"
)

// captureStorageLog sends storage error lines to a buffer, with a test clock.
func captureStorageLog(t *testing.T) (*bytes.Buffer, *time.Time) {
	t.Helper()
	var buf bytes.Buffer
	clock := time.Unix(1_800_000_000, 0)
	old := storageLog
	storageLog = &causeLog{w: &buf, now: func() time.Time { return clock }}
	t.Cleanup(func() { storageLog = old })
	return &buf, &clock
}

func TestStorageErrorLogsCauseOncePerInterval(t *testing.T) {
	s, c := transactionFixture(t)
	buf, clock := captureStorageLog(t)
	secret := "body-that-must-not-be-logged"
	execTest(t, s, "CREATE TRIGGER reject_receipt BEFORE INSERT ON operation_receipts BEGIN SELECT RAISE(ABORT,'injected receipt failure'); END")

	op := Operation{ID: "operation-logged", Name: "test.write", Scope: "session", Payload: json.RawMessage(`{"body":"` + secret + `"}`)}
	for range 3 {
		_, err := s.Mutate(ctx, c.Token, op, replayAccess, testApply(secret, false))
		requireCode(t, err, "storage_unavailable")
	}

	want := "a-stack: storage_unavailable in test.write: SQLite error 19 (constraint failed), extended code 1811\n"
	if buf.String() != want {
		t.Fatalf("log = %q, want %q", buf.String(), want)
	}

	*clock = clock.Add(storageLogInterval)
	_, err := s.Mutate(ctx, c.Token, op, replayAccess, testApply(secret, false))
	requireCode(t, err, "storage_unavailable")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 || lines[1] != strings.TrimSpace(want)+" (and 2 more times in the last 10s)" {
		t.Fatalf("log after the interval = %q", lines)
	}

	for _, leak := range []string{secret, "injected", "operation-logged", c.Token, "INSERT"} {
		if strings.Contains(buf.String(), leak) {
			t.Fatalf("log contains %q: %q", leak, buf.String())
		}
	}
}

func TestStorageErrorNamesFunctionAndErrorClass(t *testing.T) {
	buf, _ := captureStorageLog(t)
	storageError()
	storageError(&fs.PathError{Op: "open", Path: "/private/secret-path", Err: syscall.ENOSPC})
	publicError(errors.New("SELECT secret FROM somewhere"))

	got := buf.String()
	for _, want := range []string{
		"in stack.TestStorageErrorNamesFunctionAndErrorClass: cause not recorded\n",
		fmt.Sprintf("in stack.TestStorageErrorNamesFunctionAndErrorClass: *fs.PathError, errno %d (%s)\n", syscall.ENOSPC, syscall.ENOSPC.Error()),
		"in stack.TestStorageErrorNamesFunctionAndErrorClass: *errors.errorString\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("log %q lacks %q", got, want)
		}
	}

	if strings.Contains(got, "secret") {
		t.Fatalf("log leaks the error text: %q", got)
	}

	// A CLI process never calls Start, so it logs nothing.
	quiet := &causeLog{}
	quiet.write("", errors.New("x"))
}
