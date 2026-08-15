package httpx

import "github.com/kombifyio/techstack/pkg/api"

// APIError is a structured error a handler or middleware may return to control
// the rendered response envelope. It is the httpx analogue of PocketBase's
// router.ApiError: when a handler returns one, the router renders it as the
// standardized error envelope with the carried status/code/message/details.
//
// Returning a plain error instead yields a 500 INTERNAL_ERROR envelope.
type APIError struct {
	Status  int
	Code    api.ErrorCode
	Message string
	Details any
}

// Error implements the error interface.
func (e *APIError) Error() string { return e.Message }

// NewAPIError builds an APIError with an explicit status and code.
func NewAPIError(status int, code api.ErrorCode, message string, details any) *APIError {
	return &APIError{Status: status, Code: code, Message: message, Details: details}
}

// Convenience constructors mirroring the router.New*Error family.

// NewBadRequestError returns a 400 APIError.
func NewBadRequestError(message string, details any) *APIError {
	if message == "" {
		message = "Bad request"
	}
	return NewAPIError(400, api.ErrCodeBadRequest, message, details)
}

// NewUnauthorizedError returns a 401 APIError.
func NewUnauthorizedError(message string, details any) *APIError {
	if message == "" {
		message = "Authentication required"
	}
	return NewAPIError(401, api.ErrCodeUnauthorized, message, details)
}

// NewForbiddenError returns a 403 APIError.
func NewForbiddenError(message string, details any) *APIError {
	if message == "" {
		message = "Forbidden"
	}
	return NewAPIError(403, api.ErrCodeForbidden, message, details)
}

// NewNotFoundError returns a 404 APIError.
func NewNotFoundError(message string, details any) *APIError {
	if message == "" {
		message = "Not found"
	}
	return NewAPIError(404, api.ErrCodeNotFound, message, details)
}

// NewInternalServerError returns a 500 APIError.
func NewInternalServerError(message string, details any) *APIError {
	if message == "" {
		message = "Internal server error"
	}
	return NewAPIError(500, api.ErrCodeInternal, message, details)
}
