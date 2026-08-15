package api

import (
	"errors"
	"net/http"

	kserrors "github.com/kombifyio/techstack/pkg/errors"
)

// MapError maps a domain-level error into a safe, standardized HTTP status
// and API error envelope fields.
//
// IMPORTANT: This intentionally avoids including raw internal error strings
// in Details for non-validation errors, to prevent leaking implementation
// details to clients.
func MapError(err error, fallbackMessage string) (status int, code ErrorCode, message string, details any) {
	status = http.StatusInternalServerError
	code = ErrCodeInternal
	message = fallbackMessage
	details = nil

	if message == "" {
		message = "Request failed"
	}
	if err == nil {
		return
	}

	// Prefer structured validation details.
	var vErr *kserrors.ValidationError
	if errors.As(err, &vErr) {
		status = http.StatusBadRequest
		code = ErrCodeValidation
		if fallbackMessage == "" {
			message = "Validation failed"
		}
		details = map[string]any{
			"field":   vErr.Field,
			"message": vErr.Message,
			"value":   vErr.Value,
		}
		return
	}

	switch {
	case errors.Is(err, kserrors.ErrValidation):
		status = http.StatusBadRequest
		code = ErrCodeValidation
		if fallbackMessage == "" {
			message = "Validation failed"
		}
	case errors.Is(err, kserrors.ErrNotFound):
		status = http.StatusNotFound
		code = ErrCodeNotFound
		if fallbackMessage == "" {
			message = "Not found"
		}
	case errors.Is(err, kserrors.ErrUnauthorized):
		status = http.StatusUnauthorized
		code = ErrCodeUnauthorized
		if fallbackMessage == "" {
			message = "Authentication required"
		}
	case errors.Is(err, kserrors.ErrForbidden):
		status = http.StatusForbidden
		code = ErrCodeForbidden
		if fallbackMessage == "" {
			message = "Forbidden"
		}
	case errors.Is(err, kserrors.ErrAlreadyExists), errors.Is(err, kserrors.ErrConflict):
		status = http.StatusConflict
		code = ErrCodeConflict
		if fallbackMessage == "" {
			message = "Conflict"
		}
	case errors.Is(err, kserrors.ErrUnavailable):
		status = http.StatusServiceUnavailable
		code = ErrCodeUnavailable
		if fallbackMessage == "" {
			message = "Service unavailable"
		}
	case errors.Is(err, kserrors.ErrTimeout):
		status = http.StatusGatewayTimeout
		code = ErrCodeUnavailable
		if fallbackMessage == "" {
			message = "Request timed out"
		}
	default:
		// Keep safe defaults.
	}

	return
}
