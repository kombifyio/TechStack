// Package api defines shared HTTP envelope contracts for kombify Techstack.
package api

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
