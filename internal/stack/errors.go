package stack

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Error is the shared HTTP/CLI error contract. Messages never include SQL,
// request bodies, bearer credentials, or underlying filesystem diagnostics.
type Error struct {
	CurrentProgress string    `json:"current_progress,omitempty"`
	CurrentQuestion *Question `json:"current_question,omitempty"`
	Code            string    `json:"code"`
	Message         string    `json:"message"`
	Retryable       bool      `json:"retryable"`
	CurrentRevision *int64    `json:"current_revision,omitempty"`
	RetryAfter      int64     `json:"retry_after_seconds,omitempty"`
	unusable        bool
}

func (e *Error) Error() string            { return e.Code + ": " + e.Message }
func failure(code, message string) *Error { return &Error{Code: code, Message: message} }

// DatabaseUnusable reports a state database that this binary must not open:
// it failed the integrity check, or its schema is newer or unknown. serve
// exits with 4 for it, so that systemd does not restart it in a loop.
func (e *Error) DatabaseUnusable() bool { return e.unusable }
func unusableDatabase(format string, args ...any) *Error {
	return &Error{Code: "storage_unavailable", Message: fmt.Sprintf(format, args...), unusable: true}
}

// storageError is the public error of a failed storage operation. The service
// logs the cause, when the caller passes it, and the function that failed (see
// causeLog); the answer never contains either.
func storageError(cause ...error) *Error {
	var err error
	if len(cause) > 0 {
		err = cause[0]
	}
	storageLog.write("", err)
	return storageFailure()
}
func storageFailure() *Error {
	return &Error{Code: "storage_unavailable", Message: "storage operation failed; check available space, permissions and database integrity", Retryable: true}
}
func publicError(err error) *Error {
	return operationError("", err)
}

// operationError is publicError for a named operation, such as task.status; the
// log line names it instead of the Go function.
func operationError(operation string, err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	storageLog.write(operation, err)
	return storageFailure()
}

// storageLogInterval is how often the same storage error line can be logged.
const storageLogInterval = 10 * time.Second

// storageLog is quiet until Start points it at storageLogOutput, so only the
// service process logs and CLI output does not change. Tests discard it.
var storageLog = &causeLog{}
var storageLogOutput io.Writer = os.Stderr

// causeLog writes one line for each error mapped to storage_unavailable, so a
// code bug can be told apart from a full disk. A line names the operation and
// the Go error type or SQLite result codes, never a body, an ID, a credential
// or SQL text. The same line is written at most once per storageLogInterval,
// with the count of repeats it held back, so a retry loop cannot fill the
// journal.
type causeLog struct {
	mu    sync.Mutex
	w     io.Writer
	now   func() time.Time // nil means time.Now
	lines map[string]logged
}
type logged struct {
	at      time.Time
	skipped int
}

func (l *causeLog) setOutput(w io.Writer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.w = w
}
func (l *causeLog) write(operation string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.w == nil {
		return
	}
	if operation == "" {
		operation = failedFunction()
	}
	line := "a-stack: storage_unavailable in " + operation + ": " + errorClass(err)
	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	last, seen := l.lines[line]
	if seen && now.Sub(last.at) < storageLogInterval {
		last.skipped++
		l.lines[line] = last
		return
	}
	if l.lines == nil || len(l.lines) >= 256 {
		l.lines = map[string]logged{}
	}
	l.lines[line] = logged{at: now}
	if last.skipped > 0 {
		line += fmt.Sprintf(" (and %d more times in the last %s)", last.skipped, now.Sub(last.at).Round(time.Second))
	}
	_, _ = io.WriteString(l.w, line+"\n")
}

// failedFunction is the first caller outside this file, such as
// stack.(*Store).ShowTask.
func failedFunction() string {
	var pcs [8]uintptr
	frames := runtime.CallersFrames(pcs[:runtime.Callers(3, pcs[:])])
	for {
		frame, more := frames.Next()
		name := frame.Function[strings.LastIndex(frame.Function, "/")+1:]
		switch name {
		case "stack.storageError", "stack.publicError", "stack.operationError", "stack.(*causeLog).write":
		default:
			return name
		}
		if !more {
			return "unknown function"
		}
	}
}

// errorClass describes err without its message, which can hold SQL, paths or
// values. SQLite and errno texts are fixed strings for a code.
func errorClass(err error) string {
	if class := sqliteClass(err); class != "" {
		return class
	}
	var errno syscall.Errno
	var public *Error
	switch {
	case err == nil:
		return "cause not recorded"
	case errors.As(err, &errno):
		return fmt.Sprintf("%T, errno %d (%s)", err, int(errno), errno.Error())
	case errors.As(err, &public):
		return "error code " + public.Code
	}
	for _, known := range []error{context.Canceled, context.DeadlineExceeded, sql.ErrNoRows, sql.ErrTxDone, sql.ErrConnDone} {
		if errors.Is(err, known) {
			return fmt.Sprintf("%T (%s)", err, known.Error())
		}
	}
	return fmt.Sprintf("%T", err)
}
func statusCode(e *Error) int {
	switch e.Code {
	case "invalid_input":
		return http.StatusBadRequest
	case "unauthorized":
		return http.StatusUnauthorized
	case "forbidden":
		return http.StatusForbidden
	case "not_found":
		return http.StatusNotFound
	case "conflict", "operation_conflict", "session_closed":
		return http.StatusConflict
	case "rate_limited":
		return http.StatusTooManyRequests
	default:
		return http.StatusServiceUnavailable
	}
}
