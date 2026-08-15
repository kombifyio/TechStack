// Package api provides HTTP handlers and utilities for the kombifyTechstack API
package api

import (
	"encoding/json"
	"net/http"
	"time"
)

// ErrorCode represents a standardized API error code
type ErrorCode string

const (
	ErrCodeInternal         ErrorCode = "INTERNAL_ERROR"
	ErrCodeNotFound         ErrorCode = "NOT_FOUND"
	ErrCodeBadRequest       ErrorCode = "BAD_REQUEST"
	ErrCodeUnauthorized     ErrorCode = "UNAUTHORIZED"
	ErrCodeForbidden        ErrorCode = "FORBIDDEN"
	ErrCodeConflict         ErrorCode = "CONFLICT"
	ErrCodeValidation       ErrorCode = "VALIDATION_ERROR"
	ErrCodeRateLimited      ErrorCode = "RATE_LIMITED"
	ErrCodeUnavailable      ErrorCode = "SERVICE_UNAVAILABLE"
	ErrCodeMethodNotAllowed ErrorCode = "METHOD_NOT_ALLOWED"
)

// ErrorResponse represents a standardized API error response
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail contains the error details
type ErrorDetail struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	Details any       `json:"details,omitempty"`
}

// SuccessResponse represents a standardized success response
type SuccessResponse struct {
	Data any           `json:"data"`
	Meta *ResponseMeta `json:"meta,omitempty"`
}

// ManagedRuntimeLeaseGenerationDigest binds one visible RuntimeLease identity
// to the exact provider-resource generation currently held by the authority.
type ManagedRuntimeLeaseGenerationDigest struct {
	LeaseID                  string `json:"lease_id"`
	ResourceGenerationDigest string `json:"resource_generation_digest"`
}

// ResponseMeta contains metadata for API responses.
// Pagination fields are set for list endpoints; request_id and timestamp
// are injected by the response-meta middleware for all responses.
type ResponseMeta struct {
	RequestID                                string                                 `json:"request_id,omitempty"`
	Timestamp                                string                                 `json:"timestamp,omitempty"`
	Total                                    int                                    `json:"total,omitempty"`
	Page                                     int                                    `json:"page,omitempty"`
	PerPage                                  int                                    `json:"per_page,omitempty"`
	ManagedRuntimeInventoryComplete          *bool                                  `json:"managed_runtime_inventory_complete,omitempty"`
	WorkerInventorySHA256                    string                                 `json:"worker_inventory_sha256,omitempty"`
	ManagedRuntimeActiveLeaseIDs             *[]string                              `json:"managed_runtime_active_lease_ids,omitempty"`
	ManagedRuntimeInactiveLeaseIDs           *[]string                              `json:"managed_runtime_inactive_lease_ids,omitempty"`
	ManagedRuntimeLeaseGenerationDigests     *[]ManagedRuntimeLeaseGenerationDigest `json:"managed_runtime_lease_generation_digests,omitempty"`
	ManagedRuntimeDuplicateLeaseIDs          *[]string                              `json:"managed_runtime_duplicate_lease_ids,omitempty"`
	ManagedRuntimeAttachmentConflictLeaseIDs *[]string                              `json:"managed_runtime_attachment_conflict_lease_ids,omitempty"`
}

// NewResponseMeta returns a ResponseMeta pre-filled with request_id and timestamp.
// The request_id is extracted from the X-Request-ID header (set by middleware).
func NewResponseMeta(r *http.Request) *ResponseMeta {
	return &ResponseMeta{
		RequestID: r.Header.Get("X-Request-ID"),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}

// WriteJSON writes a JSON response with the given status code
func WriteJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// WriteSuccess writes a successful JSON response
func WriteSuccess(w http.ResponseWriter, data any) {
	WriteJSON(w, http.StatusOK, SuccessResponse{Data: data})
}

// WriteSuccessWithMeta writes a successful JSON response with metadata
func WriteSuccessWithMeta(w http.ResponseWriter, data any, meta *ResponseMeta) {
	WriteJSON(w, http.StatusOK, SuccessResponse{Data: data, Meta: meta})
}

// WriteError writes an error JSON response
func WriteError(w http.ResponseWriter, status int, code ErrorCode, message string, details any) {
	WriteJSON(w, status, ErrorResponse{
		Error: ErrorDetail{
			Code:    code,
			Message: message,
			Details: details,
		},
	})
}

// Common error response helpers

// NotFound writes a 404 response
func NotFound(w http.ResponseWriter, resource string) {
	WriteError(w, http.StatusNotFound, ErrCodeNotFound,
		resource+" not found", nil)
}

// BadRequest writes a 400 response
func BadRequest(w http.ResponseWriter, message string) {
	WriteError(w, http.StatusBadRequest, ErrCodeBadRequest, message, nil)
}

// InternalError writes a 500 response
func InternalError(w http.ResponseWriter, message string) {
	WriteError(w, http.StatusInternalServerError, ErrCodeInternal, message, nil)
}

// Unauthorized writes a 401 response
func Unauthorized(w http.ResponseWriter, message string) {
	WriteError(w, http.StatusUnauthorized, ErrCodeUnauthorized, message, nil)
}

// Forbidden writes a 403 response
func Forbidden(w http.ResponseWriter, message string) {
	WriteError(w, http.StatusForbidden, ErrCodeForbidden, message, nil)
}

// ValidationError writes a 400 response for validation errors
func ValidationError(w http.ResponseWriter, errors map[string]string) {
	WriteError(w, http.StatusBadRequest, ErrCodeValidation,
		"Validation failed", errors)
}

// MethodNotAllowed writes a 405 response
func MethodNotAllowed(w http.ResponseWriter, method string) {
	WriteError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed,
		"Method "+method+" not allowed", nil)
}

// DecodeJSON decodes JSON from request body
func DecodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}
