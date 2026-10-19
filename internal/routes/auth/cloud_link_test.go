package auth

import (
	"encoding/json"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func authedCloudLinkEvent(method, path, userID string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	return &httpx.Event{
		Request:  req,
		Response: rec,
		Auth:     &httpx.Principal{Id: userID},
	}, rec
}

func TestHandleCloudLinkStart_NotConfigured(t *testing.T) {
	t.Setenv("TECHSTACK_AUTH_CLOUD_CLIENT_ID", "")
	t.Setenv("AUTH0_CLIENT_ID", "")

	e, rec := authedCloudLinkEvent(http.MethodPost, "/api/v1/auth/cloud-link/start", "operator-1")
	if handlerErr := handleRuntimeCloudLinkStart(RuntimeStore{}, config.ModeSelfHosted)(e); handlerErr != nil {
		t.Fatalf("handler error: %v", handlerErr)
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), reasonCloudOIDCNotConfigured) {
		t.Fatalf("expected reason_code %q in body: %s", reasonCloudOIDCNotConfigured, rec.Body.String())
	}
}

// On SaaS the Cloud account already is the identity: cloud-link start answers
// with a structured denial instead of failing with 500, and stores no state.

func TestHandleCloudLinkStart_SaaSDeniesWithGuidance(t *testing.T) {
	userID := "operator-1"
	t.Setenv("TECHSTACK_AUTH_CLOUD_ISSUER", "https://cloud.example.test")
	t.Setenv("TECHSTACK_AUTH_CLOUD_CLIENT_ID", "test-client")

	e, rec := authedCloudLinkEvent(http.MethodPost, "/api/v1/auth/cloud-link/start", userID)
	if handlerErr := handleRuntimeCloudLinkStart(RuntimeStore{}, config.ModeSaaS)(e); handlerErr != nil {
		t.Fatalf("handler error: %v", handlerErr)
	}
	var envelope struct {
		Error struct {
			Details struct {
				ErrorCode    string         `json:"error_code"`
				Retryable    bool           `json:"retryable"`
				UserGuidance map[string]any `json:"user_guidance"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, rec.Body.String())
	}
	if rec.Code != http.StatusConflict || envelope.Error.Details.ErrorCode != cloudLinkUnavailableErrorCode ||
		envelope.Error.Details.Retryable || envelope.Error.Details.UserGuidance == nil {
		t.Fatalf("want structured cloud_link_unavailable denial, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCloudLinkCallback_UnknownStateRedirectsError(t *testing.T) {

	req := httptest.NewRequest(http.MethodGet, cloudLinkCallbackPath+"?state=unknown&code=abc", nil)
	req.AddCookie(&http.Cookie{Name: cloudLinkBindingCookie, Value: "unknown"})
	rec := httptest.NewRecorder()
	e := &httpx.Event{Request: req, Response: rec}

	if handlerErr := handleRuntimeCloudLinkCallback(RuntimeStore{}, config.ModeSelfHosted)(e); handlerErr != nil {
		t.Fatalf("handler error: %v", handlerErr)
	}
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, cloudLinkCompletePath+"#") || !strings.Contains(location, "status=error") || !strings.Contains(location, "state_expired") {
		t.Fatalf("unexpected redirect location: %q", location)
	}
}

// An unavailable cloud-link authority is a retryable, named cause the wizard
// can act on, not a bare 503.
func TestGetCloudLinkStatus_UnavailableAuthorityIsStructured(t *testing.T) {
	e, rec := authedCloudLinkEvent(http.MethodGet, "/api/v1/auth/cloud-link", "operator-1")
	if handlerErr := getRuntimeCloudLinkStatus(RuntimeStore{})(e); handlerErr != nil {
		t.Fatalf("handler error: %v", handlerErr)
	}
	var envelope struct {
		Error struct {
			Details struct {
				ReasonCode string `json:"reason_code"`
				Retryable  bool   `json:"retryable"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, rec.Body.String())
	}
	if rec.Code != http.StatusServiceUnavailable || envelope.Error.Details.ReasonCode != reasonCloudLinkAuthorityMissing ||
		!envelope.Error.Details.Retryable {
		t.Fatalf("want retryable structured authority-unavailable failure, got %d %s", rec.Code, rec.Body.String())
	}
}
