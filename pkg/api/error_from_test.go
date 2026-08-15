package api

import (
	"errors"
	"net/http"
	"testing"

	kserrors "github.com/kombifyio/techstack/pkg/errors"
)

func TestMapError_ValidationError_DetailsIncluded(t *testing.T) {
	err := kserrors.NewValidationErrorWithValue("name", "cannot be blank", "")
	status, code, message, details := MapError(err, "")

	if status != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, status)
	}
	if code != ErrCodeValidation {
		t.Fatalf("expected code %q, got %q", ErrCodeValidation, code)
	}
	if message == "" {
		t.Fatalf("expected non-empty message")
	}
	if details == nil {
		t.Fatalf("expected details for validation error")
	}
}

func TestMapError_Unauthorized_DefaultMessage(t *testing.T) {
	status, code, message, details := MapError(kserrors.ErrUnauthorized, "")

	if status != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, status)
	}
	if code != ErrCodeUnauthorized {
		t.Fatalf("expected code %q, got %q", ErrCodeUnauthorized, code)
	}
	if message == "" {
		t.Fatalf("expected non-empty message")
	}
	if details != nil {
		t.Fatalf("expected no details for unauthorized error")
	}
}

func TestMapError_Unknown_ErrorDoesNotLeakDetails(t *testing.T) {
	status, code, _, details := MapError(errors.New("boom"), "")

	if status != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, status)
	}
	if code != ErrCodeInternal {
		t.Fatalf("expected code %q, got %q", ErrCodeInternal, code)
	}
	if details != nil {
		t.Fatalf("expected details to be nil for unknown errors")
	}
}
