package api

import (
	"net/http"
	"testing"

	kserrors "github.com/kombifyio/techstack/pkg/errors"
)

func TestMapError_NotFound(t *testing.T) {
	status, code, message, details := MapError(kserrors.ErrNotFound, "")

	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
	if code != ErrCodeNotFound {
		t.Errorf("code = %q, want %q", code, ErrCodeNotFound)
	}
	if message == "" {
		t.Error("expected non-empty message")
	}
	if details != nil {
		t.Error("expected no details for not found error")
	}
}

func TestMapError_Forbidden(t *testing.T) {
	status, code, message, details := MapError(kserrors.ErrForbidden, "")

	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
	if code != ErrCodeForbidden {
		t.Errorf("code = %q, want %q", code, ErrCodeForbidden)
	}
	if message == "" {
		t.Error("expected non-empty message")
	}
	if details != nil {
		t.Error("expected no details for forbidden error")
	}
}

func TestMapError_AlreadyExists(t *testing.T) {
	status, code, message, details := MapError(kserrors.ErrAlreadyExists, "")

	if status != http.StatusConflict {
		t.Errorf("status = %d, want %d", status, http.StatusConflict)
	}
	if code != ErrCodeConflict {
		t.Errorf("code = %q, want %q", code, ErrCodeConflict)
	}
	if message == "" {
		t.Error("expected non-empty message")
	}
	if details != nil {
		t.Error("expected no details for already exists error")
	}
}

func TestMapError_Conflict(t *testing.T) {
	status, code, message, details := MapError(kserrors.ErrConflict, "")

	if status != http.StatusConflict {
		t.Errorf("status = %d, want %d", status, http.StatusConflict)
	}
	if code != ErrCodeConflict {
		t.Errorf("code = %q, want %q", code, ErrCodeConflict)
	}
	if message == "" {
		t.Error("expected non-empty message")
	}
	if details != nil {
		t.Error("expected no details for conflict error")
	}
}

func TestMapError_Unavailable(t *testing.T) {
	status, code, message, details := MapError(kserrors.ErrUnavailable, "")

	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", status, http.StatusServiceUnavailable)
	}
	if code != ErrCodeUnavailable {
		t.Errorf("code = %q, want %q", code, ErrCodeUnavailable)
	}
	if message == "" {
		t.Error("expected non-empty message")
	}
	if details != nil {
		t.Error("expected no details for unavailable error")
	}
}

func TestMapError_Timeout(t *testing.T) {
	status, code, message, details := MapError(kserrors.ErrTimeout, "")

	if status != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want %d", status, http.StatusGatewayTimeout)
	}
	if code != ErrCodeUnavailable {
		t.Errorf("code = %q, want %q", code, ErrCodeUnavailable)
	}
	if message == "" {
		t.Error("expected non-empty message")
	}
	if details != nil {
		t.Error("expected no details for timeout error")
	}
}

func TestMapError_Validation(t *testing.T) {
	status, code, message, details := MapError(kserrors.ErrValidation, "")

	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
	if code != ErrCodeValidation {
		t.Errorf("code = %q, want %q", code, ErrCodeValidation)
	}
	if message == "" {
		t.Error("expected non-empty message")
	}
	// ErrValidation (sentinel) has no details, only ValidationError struct has details
	if details != nil {
		t.Error("expected no details for sentinel validation error")
	}
}

func TestMapError_NilError(t *testing.T) {
	status, code, message, details := MapError(nil, "custom fallback")

	if status != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", status, http.StatusInternalServerError)
	}
	if code != ErrCodeInternal {
		t.Errorf("code = %q, want %q", code, ErrCodeInternal)
	}
	if message != "custom fallback" {
		t.Errorf("message = %q, want %q", message, "custom fallback")
	}
	if details != nil {
		t.Error("expected no details for nil error")
	}
}

func TestMapError_FallbackMessageUsed(t *testing.T) {
	status, code, message, details := MapError(kserrors.ErrNotFound, "Resource not found")

	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
	if code != ErrCodeNotFound {
		t.Errorf("code = %q, want %q", code, ErrCodeNotFound)
	}
	// When fallback is provided, it should be used
	if message != "Resource not found" {
		t.Errorf("message = %q, want %q", message, "Resource not found")
	}
	if details != nil {
		t.Error("expected no details")
	}
}

func TestMapError_ValidationErrorDetails(t *testing.T) {
	err := kserrors.NewValidationErrorWithValue("email", "invalid format", "bad@")
	status, code, message, details := MapError(err, "")

	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
	if code != ErrCodeValidation {
		t.Errorf("code = %q, want %q", code, ErrCodeValidation)
	}
	if message == "" {
		t.Error("expected non-empty message")
	}
	if details == nil {
		t.Fatal("expected details for ValidationError")
	}

	detailsMap, ok := details.(map[string]any)
	if !ok {
		t.Fatalf("expected details to be map[string]any, got %T", details)
	}
	if detailsMap["field"] != "email" {
		t.Errorf("details.field = %v, want %q", detailsMap["field"], "email")
	}
	if detailsMap["message"] != "invalid format" {
		t.Errorf("details.message = %v, want %q", detailsMap["message"], "invalid format")
	}
	if detailsMap["value"] != "bad@" {
		t.Errorf("details.value = %v, want %q", detailsMap["value"], "bad@")
	}
}

func TestMapError_ValidationErrorWithFallback(t *testing.T) {
	err := kserrors.NewValidationErrorWithValue("name", "required", "")
	status, code, message, _ := MapError(err, "Input validation failed")

	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
	if code != ErrCodeValidation {
		t.Errorf("code = %q, want %q", code, ErrCodeValidation)
	}
	// With fallback provided, it should be used
	if message != "Input validation failed" {
		t.Errorf("message = %q, want %q", message, "Input validation failed")
	}
}

func TestMapError_WrappedError(t *testing.T) {
	err := kserrors.Wrap(kserrors.ErrNotFound, "node lookup failed")
	status, code, _, _ := MapError(err, "")

	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
	if code != ErrCodeNotFound {
		t.Errorf("code = %q, want %q", code, ErrCodeNotFound)
	}
}

func TestMapError_WrappedValidationError(t *testing.T) {
	vErr := kserrors.NewValidationErrorWithValue("config", "invalid yaml", "bad: yaml:")
	err := kserrors.Wrap(vErr, "config parse failed")
	status, code, _, details := MapError(err, "")

	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
	if code != ErrCodeValidation {
		t.Errorf("code = %q, want %q", code, ErrCodeValidation)
	}
	if details == nil {
		t.Error("expected details for wrapped ValidationError")
	}
}
