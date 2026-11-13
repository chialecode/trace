package site

import (
	"errors"
	"fmt"
)

// Unified error classifications shared across all site adapters and managers.
const (
	ErrKindAuthRequired    = "auth_required"
	ErrKindRiskControl     = "risk_control"
	ErrKindRateLimited     = "rate_limited"
	ErrKindBlocked         = "blocked"
	ErrKindUnavailable     = "unavailable"
	ErrKindForbidden       = "forbidden"
	ErrKindBadRequest      = "bad_request"
	ErrKindSchemaChanged   = "schema_changed"
	ErrKindNetwork         = "network"
	ErrKindQuota           = "quota"
	ErrKindBackoff         = "backoff"
	ErrKindStorage         = "storage"
	ErrKindConflict        = "conflict"
	ErrKindInvalidInput    = "invalid_input"
	ErrKindUnsupportedLink = "unsupported_link"
)

// Error is a structured error returned by site adapters and dispatchers.
// When Site is omitted, Error() defaults the prefix to "bilibili" for backward compatibility.
type Error struct {
	Site        ID     `json:"site,omitempty"`
	Kind        string `json:"kind"`
	Code        int    `json:"code,omitempty"`
	HTTPStatus  int    `json:"httpStatus,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
	Unsupported bool   `json:"unsupported,omitempty"`
	Message     string `json:"message"`
	Err         error  `json:"-"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	prefix := string(e.Site)
	if prefix == "" {
		prefix = string(Bilibili)
	}
	if e.Code != 0 {
		return fmt.Sprintf("%s [%s code=%d]: %s", prefix, e.Kind, e.Code, e.Message)
	}
	if e.HTTPStatus != 0 {
		return fmt.Sprintf("%s [%s http=%d]: %s", prefix, e.Kind, e.HTTPStatus, e.Message)
	}
	return fmt.Sprintf("%s [%s]: %s", prefix, e.Kind, e.Message)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// KindOf extracts the error classification from err, defaulting to ErrKindNetwork.
func KindOf(err error) string {
	if err == nil {
		return ""
	}
	var se *Error
	if errors.As(err, &se) && se.Kind != "" {
		return se.Kind
	}
	return ErrKindNetwork
}

// IsKind reports whether err is a *Error of the specified classification.
func IsKind(err error, kind string) bool {
	var se *Error
	return errors.As(err, &se) && se.Kind == kind
}

// SiteOf extracts the site ID recorded on err, or empty ID if unset.
func SiteOf(err error) ID {
	var se *Error
	if errors.As(err, &se) {
		return se.Site
	}
	return ""
}

// IsUnsupported reports whether err represents an explicitly unsupported link or content form.
func IsUnsupported(err error) bool {
	var se *Error
	return errors.As(err, &se) && (se.Unsupported || se.Kind == ErrKindUnsupportedLink)
}

// IsRoundStop reports whether err requires immediately aborting the current background check round.
func IsRoundStop(err error) bool {
	switch KindOf(err) {
	case ErrKindAuthRequired, ErrKindRiskControl, ErrKindRateLimited:
		return true
	default:
		return false
	}
}

// IsCircuitBreak reports whether err should trip the automatic-request circuit breaker.
func IsCircuitBreak(err error) bool {
	switch KindOf(err) {
	case ErrKindRiskControl, ErrKindRateLimited:
		return true
	default:
		return false
	}
}
