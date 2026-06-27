package store

import (
	"errors"
	"fmt"
)

// FieldError is a structured, LLM-friendly error that pins which input
// argument is at fault, the offending value (truncated if huge), a
// stable machine code, and a one-line hint for the caller. Returned by
// store + service layers when an input that looks like a tool argument
// failed validation or triggered a known backend rejection (e.g. FTS5
// syntax errors). The gateway handler unwraps this with errors.As and
// emits a structured tool-result body so the LLM sees a typed envelope
// instead of a nested wrapper string.
type FieldError struct {
	Code    string // stable machine identifier, e.g. "fts5_reserved_syntax"
	Message string // human-readable one-liner (no nested wrapping)
	Field   string // argument name, e.g. "q" or "id"
	Value   string // offending value as the caller passed it
	Hint    string // one-line remediation hint
	Example string // optional valid example
	Cause   error  // optional wrapped error for debugging only
}

// Error renders the structured error as a single line. The gateway
// caller serialises FieldError as JSON for the tool result so this
// string form is mainly for logs.
func (e *FieldError) Error() string {
	if e == nil {
		return ""
	}
	if e.Field != "" {
		return fmt.Sprintf("%s: %s (field=%s value=%q)", e.Code, e.Message, e.Field, e.Value)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the underlying cause so errors.Is / errors.As keep
// working through the wrapping.
func (e *FieldError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// NewFieldError builds a FieldError with the given code/field/value/hint.
// The value is truncated to a reasonable length so very large blobs
// don't blow up the tool-result payload.
func NewFieldError(code, field, value, message, hint string) *FieldError {
	const maxValueLen = 512
	if len(value) > maxValueLen {
		value = value[:maxValueLen] + "...(truncated)"
	}
	return &FieldError{
		Code:    code,
		Field:   field,
		Value:   value,
		Message: message,
		Hint:    hint,
	}
}

var (
	// ErrNotFound indicates the requested resource does not exist.
	ErrNotFound = errors.New("not found")

	// ErrAlreadyExists indicates the resource already exists (unique constraint).
	ErrAlreadyExists = errors.New("already exists")

	// ErrConflict indicates a concurrent modification conflict.
	ErrConflict = errors.New("conflict")

	// ErrWorkerNotFound is returned when a Worker row is missing. Distinct
	// from ErrNotFound so callers can tell the agent "the worker doesn't
	// exist" without false-positives on related lookups.
	ErrWorkerNotFound = errors.New("worker not found")

	// ErrWorkerArchived is returned when a caller tries to run or re-enable
	// a worker that has been explicitly archived. Archived workers retain
	// their row and run history but must never dispatch.
	ErrWorkerArchived = errors.New("worker archived")

	// ErrWorkerRunNotFound is returned when a WorkerRun row is missing.
	ErrWorkerRunNotFound = errors.New("worker run not found")

	// ErrWorkerApprovalNotFound is returned when a WorkerApproval row
	// is missing.
	ErrWorkerApprovalNotFound = errors.New("worker approval not found")

	// ErrWorkerMeshTriggerNotFound is returned when a WorkerMeshTrigger
	// row is missing (M4).
	ErrWorkerMeshTriggerNotFound = errors.New("worker mesh trigger not found")

	// ErrRunNotCancellable is returned by CancelRun when the row exists
	// but is already in a terminal status (success, failure,
	// cap_exceeded, awaiting_approval, rejected). Distinct from
	// ErrWorkerRunNotFound so the admin surface can return a different
	// message ("already finished" vs "doesn't exist").
	ErrRunNotCancellable = errors.New("worker run not cancellable")

	// ErrTaskAlreadyClaimed is returned by ClaimTask when the CAS guard
	// detects that another session claimed the task first. Distinct from
	// ErrConflict so callers can surface the "you lost the race" signal
	// without conflating it with other concurrent-modification errors.
	ErrTaskAlreadyClaimed = errors.New("task already claimed by another session")
)
