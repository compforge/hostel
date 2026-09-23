// Package hostel defines protocol-independent failures shared across Hostel.
// Domain packages attach context; API adapters own status codes and wire shapes.
package hostel

import "fmt"

// ErrorKind is a stable category and an errors.Is target. Cancellation and deadlines
// use context.Canceled/DeadlineExceeded instead of duplicate categories.
type ErrorKind string

const (
	ErrInvalidArgument   ErrorKind = "invalid argument"
	ErrNotFound          ErrorKind = "not found"
	ErrConflict          ErrorKind = "conflict"
	ErrUnavailable       ErrorKind = "unavailable"
	ErrPreparationFailed ErrorKind = "preparation failed"
	ErrLimitExceeded     ErrorKind = "limit exceeded"
)

func (k ErrorKind) Error() string { return string(k) }

// Error preserves the underlying failure while exposing a stable category.
// Op describes an operation, e.g. "session directory"; it must not contain
// commands, credentials, user values or carrier paths.
type Error struct {
	Kind ErrorKind
	Op   string
	Err  error
}

func WrapError(kind ErrorKind, op string, cause error) *Error {
	return &Error{Kind: kind, Op: op, Err: cause}
}
func (e *Error) Error() string {
	if e.Err == nil {
		return e.Message()
	}
	return fmt.Sprintf("%s: %v", e.Message(), e.Err)
}
func (e *Error) Unwrap() error        { return e.Err }
func (e *Error) Is(target error) bool { return target == e.Kind }

// Message omits the internal cause so adapters can expose a safe description.
func (e *Error) Message() string {
	if e.Op == "" {
		return e.Kind.Error()
	}
	return e.Op + ": " + e.Kind.Error()
}
