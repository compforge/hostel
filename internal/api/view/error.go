package view

// ErrorCode mirrors the OpenSandbox execd error vocabulary so SDK error
// handling works against hostel unchanged.
type ErrorCode string

const (
	ErrInvalidRequest     ErrorCode = "INVALID_REQUEST_BODY"
	ErrMissingQuery       ErrorCode = "MISSING_QUERY"
	ErrRuntimeError       ErrorCode = "RUNTIME_ERROR"
	ErrFileNotFound       ErrorCode = "FILE_NOT_FOUND"
	ErrMCPServerNotFound  ErrorCode = "MCP_SERVER_NOT_FOUND"
	ErrMCPCapacity        ErrorCode = "MCP_CAPACITY_EXCEEDED"
	ErrMCPTimeout         ErrorCode = "MCP_TIMEOUT"
	ErrMCPUpstream        ErrorCode = "MCP_UPSTREAM_ERROR"
	ErrNotSupported       ErrorCode = "NOT_SUPPORTED"
	ErrSessionNotFound    ErrorCode = "SESSION_NOT_FOUND"
	ErrCommandNotFound    ErrorCode = "COMMAND_NOT_FOUND"
	ErrBedInvalid         ErrorCode = "BED_INVALID"
	ErrBedLimitExceeded   ErrorCode = "BED_LIMIT_EXCEEDED"
	ErrResourcePressure   ErrorCode = "RESOURCE_PRESSURE"
	ErrBedBusy            ErrorCode = "BED_BUSY"
	ErrBedSyncConflict    ErrorCode = "BED_SYNC_CONFLICT"
	ErrServiceUnavailable ErrorCode = "SERVICE_UNAVAILABLE"
)

// ErrorResponse keeps execd's required code/message envelope and adds optional
// hostel scheduler hints that existing OpenSandbox SDKs can ignore.
type ErrorResponse struct {
	Code      ErrorCode `json:"code,omitempty"`
	Message   string    `json:"message,omitempty"`
	Retryable bool      `json:"retryable,omitempty"`
}
