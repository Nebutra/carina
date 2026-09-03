package browser

import "fmt"

type ErrorCode string

const (
	ErrorUnavailable    ErrorCode = "browser_unavailable"
	ErrorInvalidRequest ErrorCode = "browser_invalid_request"
	ErrorAlreadyOpen    ErrorCode = "browser_already_open"
	ErrorNotFound       ErrorCode = "browser_not_found"
	ErrorStaleRef       ErrorCode = "browser_stale_ref"
	ErrorResourceLimit  ErrorCode = "browser_resource_limit"
	ErrorNetworkBlocked ErrorCode = "browser_network_blocked"
	ErrorCancelled      ErrorCode = "browser_cancelled"
	ErrorDriver         ErrorCode = "browser_driver_error"
)

type Error struct {
	Code      ErrorCode
	Message   string
	Retryable bool
	Recovery  string
	Cause     error
}

func (e *Error) Error() string {
	if e == nil {
		return "browser error"
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func browserError(code ErrorCode, message, recovery string, retryable bool, cause error) error {
	return &Error{Code: code, Message: message, Recovery: recovery, Retryable: retryable, Cause: cause}
}
