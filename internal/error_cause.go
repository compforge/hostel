package hostel

// ErrorCause identifies the evidenced source of a failure, independently of
// ErrorKind and the operation reporting it. It is not a retry policy.
type ErrorCause string

const (
	CauseUnknown  ErrorCause = "unknown"
	CauseCarrier  ErrorCause = "carrier"
	CauseBed      ErrorCause = "bed"
	CauseRequest  ErrorCause = "request"
	CauseResource ErrorCause = "resource"
)

// CauseOf reads attribution through standard error wrapping. An unclassified
// failure stays unknown; location, error text and HTTP status are not evidence.
// Joined failures with different attributions have no single cause.
//
// +spec=`Error attribution survives wrapping; mixed joined failures remain unknown.`
// +link=component://docs/protocol.md
func CauseOf(err error) ErrorCause {
	if err == nil {
		return CauseUnknown
	}
	if detail, ok := err.(*Error); ok && detail.Cause != "" && detail.Cause != CauseUnknown {
		return detail.Cause
	}
	switch e := err.(type) {
	case interface{ Unwrap() []error }:
		cause := ErrorCause("")
		for _, child := range e.Unwrap() {
			if child == nil {
				continue
			}
			next := CauseOf(child)
			if cause != "" && cause != next {
				return CauseUnknown
			}
			cause = next
		}
		if cause != "" {
			return cause
		}
	case interface{ Unwrap() error }:
		return CauseOf(e.Unwrap())
	}
	return CauseUnknown
}
