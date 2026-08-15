package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteError_EnvelopeShape(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteError(rr, http.StatusBadRequest, ErrCodeBadRequest, "Bad input", map[string]any{"field": "name"})

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}

	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}

	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	errObj, ok := got["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected top-level 'error' object, got: %#v", got)
	}

	if errObj["code"] != string(ErrCodeBadRequest) {
		t.Fatalf("expected error.code %q, got %#v", ErrCodeBadRequest, errObj["code"])
	}
	if errObj["message"] != "Bad input" {
		t.Fatalf("expected error.message %q, got %#v", "Bad input", errObj["message"])
	}
	if _, ok := errObj["details"]; !ok {
		t.Fatalf("expected error.details to exist")
	}
}

func TestWriteError_OmitsNilDetails(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteError(rr, http.StatusUnauthorized, ErrCodeUnauthorized, "Authentication required", nil)

	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	errObj, ok := got["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected top-level 'error' object")
	}

	if _, ok := errObj["details"]; ok {
		t.Fatalf("expected error.details to be omitted when nil")
	}
}

func TestWriteSuccess_EnvelopeShape(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteSuccess(rr, map[string]any{"status": "ok"})

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if _, ok := got["data"]; !ok {
		t.Fatalf("expected top-level 'data' field")
	}
	if _, ok := got["error"]; ok {
		t.Fatalf("did not expect top-level 'error' on success")
	}
}
