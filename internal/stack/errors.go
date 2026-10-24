package stack

import (
	"errors"
	"net/http"
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
}

func (e *Error) Error() string            { return e.Code + ": " + e.Message }
func failure(code, message string) *Error { return &Error{Code: code, Message: message} }
func storageError() *Error {
	return &Error{Code: "storage_unavailable", Message: "storage operation failed; check available space, permissions and database integrity", Retryable: true}
}
func publicError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return storageError()
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
	default:
		return http.StatusServiceUnavailable
	}
}
