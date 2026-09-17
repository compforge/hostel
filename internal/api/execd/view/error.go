package view

type ErrorCode string

const (
	ErrInvalidRequest     ErrorCode = "INVALID_REQUEST_BODY"
	ErrMissingQuery       ErrorCode = "MISSING_QUERY"
	ErrRuntimeError       ErrorCode = "RUNTIME_ERROR"
	ErrFileNotFound       ErrorCode = "FILE_NOT_FOUND"
	ErrNotSupported       ErrorCode = "NOT_SUPPORTED"
	ErrSessionNotFound    ErrorCode = "SESSION_NOT_FOUND"
	ErrCommandNotFound    ErrorCode = "COMMAND_NOT_FOUND"
	ErrBedInvalid         ErrorCode = "BED_INVALID"
	ErrServiceUnavailable ErrorCode = "SERVICE_UNAVAILABLE"
)

type ErrorResponse struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}
