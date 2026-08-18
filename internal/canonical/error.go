package canonical

import (
	"fmt"
	"net/http"
)

// ErrorType is a protocol-neutral error class. Each protocol encoder maps it
// onto its own vocabulary.
type ErrorType string

const (
	ErrInvalidRequest ErrorType = "invalid_request"
	ErrAuthentication ErrorType = "authentication"
	ErrPermission     ErrorType = "permission"
	ErrNotFound       ErrorType = "not_found"
	ErrRateLimit      ErrorType = "rate_limit"
	ErrOverloaded     ErrorType = "overloaded"
	ErrUpstream       ErrorType = "upstream"
	ErrTimeout        ErrorType = "timeout"
	ErrInternal       ErrorType = "internal"
	ErrUnsupported    ErrorType = "unsupported"
)

// Error is the canonical error. Message is safe to return to the client;
// callers must never place upstream credentials in it.
type Error struct {
	Type    ErrorType `json:"type"`
	Message string    `json:"message"`
	Code    string    `json:"code,omitempty"`
	Param   string    `json:"param,omitempty"`
	// StatusCode is the HTTP status Polyglot should reply with.
	StatusCode int `json:"status_code,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Type, e.Message) }

func (e *Error) Status() int {
	if e.StatusCode != 0 {
		return e.StatusCode
	}
	return StatusForType(e.Type)
}

func StatusForType(t ErrorType) int {
	switch t {
	case ErrInvalidRequest, ErrUnsupported:
		return http.StatusBadRequest
	case ErrAuthentication:
		return http.StatusUnauthorized
	case ErrPermission:
		return http.StatusForbidden
	case ErrNotFound:
		return http.StatusNotFound
	case ErrRateLimit:
		return http.StatusTooManyRequests
	case ErrOverloaded:
		return http.StatusServiceUnavailable
	case ErrTimeout:
		return http.StatusGatewayTimeout
	case ErrUpstream:
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

// TypeForStatus maps an upstream HTTP status back onto a canonical class.
func TypeForStatus(status int) ErrorType {
	switch {
	case status == http.StatusUnauthorized:
		return ErrAuthentication
	case status == http.StatusForbidden:
		return ErrPermission
	case status == http.StatusNotFound:
		return ErrNotFound
	case status == http.StatusTooManyRequests:
		return ErrRateLimit
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return ErrTimeout
	case status == http.StatusServiceUnavailable:
		return ErrOverloaded
	case status >= 400 && status < 500:
		return ErrInvalidRequest
	default:
		return ErrUpstream
	}
}

func Errorf(t ErrorType, format string, args ...any) *Error {
	return &Error{Type: t, Message: fmt.Sprintf(format, args...)}
}
