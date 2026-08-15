package httpx

import (
	"errors"
	"time"

	"github.com/kombifyio/techstack/pkg/api"
)

// This file mirrors pkg/api/pocketbase.go (the PBSuccess/PBError family) but
// takes *httpx.Event instead of *core.RequestEvent. The emitted wire format is
// byte-identical: it reuses api.SuccessResponse / api.ErrorResponse /
// api.ResponseMeta / api.ErrorCode so migrated routes produce the exact same
// JSON envelopes. These are the single envelope sink for httpx routes.
//
// Naming maps 1:1 onto the PB helpers so migration is a mechanical rename:
//
//	api.PBSuccess(e, ...)         -> httpx.Success(e, ...)
//	api.PBSuccessWithMeta(e, ...) -> httpx.SuccessWithMeta(e, ...)
//	api.PBError(e, ...)           -> httpx.Error(e, ...)
//	api.PBBadRequest(e, ...)      -> httpx.BadRequest(e, ...)
//	api.PBUnauthorized(e, ...)    -> httpx.Unauthorized(e, ...)
//	api.PBForbidden(e, ...)       -> httpx.Forbidden(e, ...)
//	api.PBNotFound(e, ...)        -> httpx.NotFound(e, ...)
//	api.PBMethodNotAllowed(e, ...)-> httpx.MethodNotAllowed(e, ...)

// Success writes a standardized success envelope and injects request_id +
// timestamp into meta. Mirrors api.PBSuccess.
func Success(e *Event, status int, data any) error {
	meta := &api.ResponseMeta{
		RequestID: e.Request.Header.Get("X-Request-ID"),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	return e.JSON(status, api.SuccessResponse{Data: data, Meta: meta})
}

// SuccessWithMeta writes a standardized success envelope, merging request_id +
// timestamp into the provided meta. Mirrors api.PBSuccessWithMeta.
func SuccessWithMeta(e *Event, status int, data any, meta *api.ResponseMeta) error {
	if meta == nil {
		meta = &api.ResponseMeta{}
	}
	meta.RequestID = e.Request.Header.Get("X-Request-ID")
	meta.Timestamp = time.Now().UTC().Format(time.RFC3339)
	return e.JSON(status, api.SuccessResponse{Data: data, Meta: meta})
}

// Error writes a standardized error envelope. Mirrors api.PBError.
func Error(e *Event, status int, code api.ErrorCode, message string, details any) error {
	if status >= 500 {
		captureInternalServerError(e, message, map[string]any{
			"status":  status,
			"code":    code,
			"details": details,
		})
	}
	return e.JSON(status, api.ErrorResponse{
		Error: api.ErrorDetail{Code: code, Message: message, Details: details},
	})
}

// Reject writes a rejection envelope and returns a NON-NIL error.
//
// Use it in any helper shaped `func(...) (T, error)` that writes a refusal and
// hands the error to a caller. Error returns e.JSON's error, which is nil
// whenever the write succeeds, so such a helper reported success after already
// writing a 403 - and a caller written as `if err != nil { return err }` did
// not stop. That reached production on GET /metrics and GET /api/v1/metrics
// through requireAdmin, which discards the boolean that was the real signal.
//
// Terminal handlers that simply `return httpx.Error(...)` are unaffected and
// keep using Error. renderError skips events whose response is already
// written, so a returned sentinel never produces a second envelope.
func Reject(e *Event, status int, code api.ErrorCode, message string, details any) error {
	if err := Error(e, status, code, message, details); err != nil {
		return err
	}
	return ErrResponseWritten
}

// ErrResponseWritten is returned by Error after a rejection envelope has been
// written. It exists so `if err != nil` stops the caller. Handlers may return
// it unchanged; the router recognises it and writes nothing further.
var ErrResponseWritten = errors.New("httpx: response already written")

// RejectUnauthorized writes a 401 envelope and returns a non-nil error.
// Use it instead of Unauthorized in any helper that hands its error to a
// caller; see Reject for why.
func RejectUnauthorized(e *Event, message string) error {
	if message == "" {
		message = "Authentication required"
	}
	return Reject(e, 401, api.ErrCodeUnauthorized, message, nil)
}

// RejectForbidden writes a 403 envelope and returns a non-nil error.
// Use it instead of Forbidden in any helper that hands its error to a caller.
func RejectForbidden(e *Event, message string) error {
	if message == "" {
		message = "Forbidden"
	}
	return Reject(e, 403, api.ErrCodeForbidden, message, nil)
}

// BadRequest writes a 400 envelope. Mirrors api.PBBadRequest.
func BadRequest(e *Event, message string, details ...any) error {
	if len(details) > 0 {
		return Error(e, 400, api.ErrCodeBadRequest, message, details[0])
	}
	return Error(e, 400, api.ErrCodeBadRequest, message, nil)
}

// Unauthorized writes a 401 envelope. Mirrors api.PBUnauthorized.
func Unauthorized(e *Event, message string) error {
	if message == "" {
		message = "Authentication required"
	}
	return Error(e, 401, api.ErrCodeUnauthorized, message, nil)
}

// Forbidden writes a 403 envelope. Mirrors api.PBForbidden.
func Forbidden(e *Event, message string) error {
	if message == "" {
		message = "Forbidden"
	}
	return Error(e, 403, api.ErrCodeForbidden, message, nil)
}

// NotFound writes a 404 envelope. Mirrors api.PBNotFound.
func NotFound(e *Event, message string) error {
	if message == "" {
		message = "Not found"
	}
	return Error(e, 404, api.ErrCodeNotFound, message, nil)
}

// MethodNotAllowed writes a 405 envelope. Mirrors api.PBMethodNotAllowed.
func MethodNotAllowed(e *Event, method string) error {
	return Error(e, 405, api.ErrCodeMethodNotAllowed, "Method "+method+" not allowed", nil)
}

// InternalError writes a 500 envelope. Convenience for the error renderer and
// handlers that need an explicit server-error envelope.
func InternalError(e *Event, message string) error {
	if message == "" {
		message = "Internal server error"
	}
	captureInternalServerError(e, message, nil)
	return Error(e, 500, api.ErrCodeInternal, message, nil)
}

// defaultNotFound is the Router's fallback no-route handler.
func defaultNotFound(e *Event) error {
	return NotFound(e, "Not found")
}

// defaultMethodNotAllowed is the Router's fallback wrong-method handler.
func defaultMethodNotAllowed(e *Event) error {
	return MethodNotAllowed(e, e.Request.Method)
}

// renderError converts a handler/middleware error into a response envelope when
// nothing has been written yet. It honors *APIError for status/code/message and
// falls back to a 500 INTERNAL_ERROR otherwise. Mirrors how PocketBase's router
// renders a returned ApiError.
func renderError(e *Event, err error) {
	if err == nil {
		return
	}
	// Honour the contract stated above. Error() now returns a non-nil sentinel
	// so that helpers which write a rejection and hand the error back cannot be
	// mistaken for success; without this guard that sentinel would append a
	// second envelope to every handler that ends in `return httpx.Error(...)`.
	if e.ResponseWritten() {
		return
	}
	if apiErr, ok := err.(*APIError); ok {
		_ = Error(e, apiErr.Status, apiErr.Code, apiErr.Message, apiErr.Details)
		return
	}
	_ = InternalError(e, err.Error())
}
