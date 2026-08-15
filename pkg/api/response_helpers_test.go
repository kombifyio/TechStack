package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteSuccessWithMeta(t *testing.T) {
	rr := httptest.NewRecorder()
	meta := &ResponseMeta{Total: 50, Page: 2, PerPage: 10}
	WriteSuccessWithMeta(rr, []string{"item1", "item2"}, meta)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, rr.Code)
	}

	var resp SuccessResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if resp.Meta == nil {
		t.Fatal("expected meta to be present")
	}
	if resp.Meta.Total != 50 {
		t.Errorf("meta.Total = %d, want 50", resp.Meta.Total)
	}
	if resp.Meta.Page != 2 {
		t.Errorf("meta.Page = %d, want 2", resp.Meta.Page)
	}
	if resp.Meta.PerPage != 10 {
		t.Errorf("meta.PerPage = %d, want 10", resp.Meta.PerPage)
	}
}

func TestNotFound(t *testing.T) {
	rr := httptest.NewRecorder()
	NotFound(rr, "User")

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, rr.Code)
	}

	var resp ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if resp.Error.Code != ErrCodeNotFound {
		t.Errorf("error.code = %q, want %q", resp.Error.Code, ErrCodeNotFound)
	}
	if !strings.Contains(resp.Error.Message, "User") {
		t.Errorf("error.message should contain 'User', got %q", resp.Error.Message)
	}
}

func TestBadRequest(t *testing.T) {
	rr := httptest.NewRecorder()
	BadRequest(rr, "invalid input")

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}

	var resp ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if resp.Error.Code != ErrCodeBadRequest {
		t.Errorf("error.code = %q, want %q", resp.Error.Code, ErrCodeBadRequest)
	}
	if resp.Error.Message != "invalid input" {
		t.Errorf("error.message = %q, want %q", resp.Error.Message, "invalid input")
	}
}

func TestInternalError(t *testing.T) {
	rr := httptest.NewRecorder()
	InternalError(rr, "something went wrong")

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("expected status %d, got %d", http.StatusInternalServerError, rr.Code)
	}

	var resp ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if resp.Error.Code != ErrCodeInternal {
		t.Errorf("error.code = %q, want %q", resp.Error.Code, ErrCodeInternal)
	}
	if resp.Error.Message != "something went wrong" {
		t.Errorf("error.message = %q, want %q", resp.Error.Message, "something went wrong")
	}
}

func TestUnauthorized(t *testing.T) {
	rr := httptest.NewRecorder()
	Unauthorized(rr, "invalid token")

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}

	var resp ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if resp.Error.Code != ErrCodeUnauthorized {
		t.Errorf("error.code = %q, want %q", resp.Error.Code, ErrCodeUnauthorized)
	}
	if resp.Error.Message != "invalid token" {
		t.Errorf("error.message = %q, want %q", resp.Error.Message, "invalid token")
	}
}

func TestForbidden(t *testing.T) {
	rr := httptest.NewRecorder()
	Forbidden(rr, "access denied")

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected status %d, got %d", http.StatusForbidden, rr.Code)
	}

	var resp ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if resp.Error.Code != ErrCodeForbidden {
		t.Errorf("error.code = %q, want %q", resp.Error.Code, ErrCodeForbidden)
	}
	if resp.Error.Message != "access denied" {
		t.Errorf("error.message = %q, want %q", resp.Error.Message, "access denied")
	}
}

func TestValidationError(t *testing.T) {
	rr := httptest.NewRecorder()
	errors := map[string]string{
		"email": "invalid format",
		"name":  "required",
	}
	ValidationError(rr, errors)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, rr.Code)
	}

	var resp ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if resp.Error.Code != ErrCodeValidation {
		t.Errorf("error.code = %q, want %q", resp.Error.Code, ErrCodeValidation)
	}
	if resp.Error.Message != "Validation failed" {
		t.Errorf("error.message = %q, want %q", resp.Error.Message, "Validation failed")
	}
	if resp.Error.Details == nil {
		t.Error("expected details to be present")
	}
}

func TestMethodNotAllowed(t *testing.T) {
	rr := httptest.NewRecorder()
	MethodNotAllowed(rr, "DELETE")

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status %d, got %d", http.StatusMethodNotAllowed, rr.Code)
	}

	var resp ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if resp.Error.Code != ErrCodeMethodNotAllowed {
		t.Errorf("error.code = %q, want %q", resp.Error.Code, ErrCodeMethodNotAllowed)
	}
	if !strings.Contains(resp.Error.Message, "DELETE") {
		t.Errorf("error.message should contain 'DELETE', got %q", resp.Error.Message)
	}
}

func TestDecodeJSON_Success(t *testing.T) {
	body := `{"name": "test", "value": 42}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))

	var data struct {
		Name  string `json:"name"`
		Value int    `json:"value"`
	}

	err := DecodeJSON(req, &data)
	if err != nil {
		t.Fatalf("DecodeJSON failed: %v", err)
	}

	if data.Name != "test" {
		t.Errorf("data.Name = %q, want %q", data.Name, "test")
	}
	if data.Value != 42 {
		t.Errorf("data.Value = %d, want %d", data.Value, 42)
	}
}

func TestDecodeJSON_InvalidJSON(t *testing.T) {
	body := `{invalid json}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))

	var data struct{}
	err := DecodeJSON(req, &data)
	if err == nil {
		t.Error("expected DecodeJSON to fail for invalid JSON")
	}
}

func TestDecodeJSON_EmptyBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))

	var data struct{}
	err := DecodeJSON(req, &data)
	if err == nil {
		t.Error("expected DecodeJSON to fail for empty body")
	}
}

func TestWriteJSON_CustomStatus(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteJSON(rr, http.StatusCreated, map[string]string{"id": "123"})

	if rr.Code != http.StatusCreated {
		t.Errorf("expected status %d, got %d", http.StatusCreated, rr.Code)
	}

	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}

	var data map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &data); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if data["id"] != "123" {
		t.Errorf("data[id] = %q, want %q", data["id"], "123")
	}
}
