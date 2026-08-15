package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/clientpairing"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

const (
	clientPairingTestTenant      = "tenant-home"
	clientPairingTestFingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

var clientPairingTestNow = time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

func TestClientPairingIssueRedeemAndReplay(t *testing.T) {
	router := httpx.NewRouter()
	config := clientPairingTestConfig(clientDeploymentModeSelfHosted)
	RegisterClientPairingRoutes(router, config)

	issueRequest := httptest.NewRequest(http.MethodPost, "https://techstack.home.example"+clientPairingIssuePath, nil)
	issueRequest = issueRequest.WithContext(identity.NewContext(context.Background(), &identity.Identity{
		UserID: "owner-1", OrgID: clientPairingTestTenant, Roles: []string{"admin"},
	}))
	issueRecorder := httptest.NewRecorder()
	router.ServeHTTP(issueRecorder, issueRequest)
	if issueRecorder.Code != http.StatusCreated {
		t.Fatalf("issue status = %d body=%s", issueRecorder.Code, issueRecorder.Body.String())
	}
	if issueRecorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("issue Cache-Control = %q", issueRecorder.Header().Get("Cache-Control"))
	}
	var envelope clientpairing.Envelope
	if err := json.NewDecoder(issueRecorder.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Version != "1" || envelope.Endpoint != "https://techstack.home.example"+clientpairing.RedeemPath || envelope.InstanceID != clientDiscoveryTestInstanceID {
		t.Fatalf("envelope = %#v", envelope)
	}
	if envelope.ExpiresAt.Sub(envelope.IssuedAt) != clientpairing.DefaultLifetime {
		t.Fatalf("lifetime = %s", envelope.ExpiresAt.Sub(envelope.IssuedAt))
	}

	redeemBody, _ := json.Marshal(clientPairingRedeemRequest{
		Version: "1", InstanceID: envelope.InstanceID,
		TLSFingerprintSHA256: envelope.TLSFingerprintSHA256, OneTimeCode: envelope.OneTimeCode,
	})
	redeemRecorder := httptest.NewRecorder()
	router.ServeHTTP(redeemRecorder, httptest.NewRequest(http.MethodPost, envelope.Endpoint, bytes.NewReader(redeemBody)))
	if redeemRecorder.Code != http.StatusOK {
		t.Fatalf("redeem status = %d body=%s", redeemRecorder.Code, redeemRecorder.Body.String())
	}
	redeemResponseBody := append([]byte(nil), redeemRecorder.Body.Bytes()...)
	var response clientPairingRedeemResponse
	if err := json.Unmarshal(redeemResponseBody, &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != clientPairingStatusDone || response.WorkspaceID != clientPairingTestTenant || response.AccountHandoff.Kind != "oidc" {
		t.Fatalf("redeem response = %#v", response)
	}
	if response.AccountHandoff.Issuer != config.Profile.OIDCIssuer || response.AccountHandoff.ClientID != config.Profile.OIDCClientID || !reflect.DeepEqual(response.AccountHandoff.Scopes, config.Profile.OIDCScopes) {
		t.Fatalf("account handoff = %#v", response.AccountHandoff)
	}
	var raw map[string]any
	if err := json.Unmarshal(redeemResponseBody, &raw); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"token", "session", "password", "credential", "user_id"} {
		if _, exists := raw[forbidden]; exists {
			t.Fatalf("redeem response contains forbidden authority field %q", forbidden)
		}
	}

	replayRecorder := httptest.NewRecorder()
	router.ServeHTTP(replayRecorder, httptest.NewRequest(http.MethodPost, envelope.Endpoint, bytes.NewReader(redeemBody)))
	if replayRecorder.Code != http.StatusConflict {
		t.Fatalf("replay status = %d body=%s", replayRecorder.Code, replayRecorder.Body.String())
	}
	assertClientPairingReason(t, replayRecorder, "pairing_code_already_consumed")
}

func TestClientPairingRejectsFingerprintBeforeConsume(t *testing.T) {
	router := httpx.NewRouter()
	config := clientPairingTestConfig(clientDeploymentModeSelfHosted)
	RegisterClientPairingRoutes(router, config)
	envelope := issueClientPairingForRouteTest(t, router)

	wrongInstanceBody, _ := json.Marshal(clientPairingRedeemRequest{
		Version: "1", InstanceID: "99999999-2222-4333-8444-555555555555",
		TLSFingerprintSHA256: envelope.TLSFingerprintSHA256,
		OneTimeCode:          envelope.OneTimeCode,
	})
	wrongInstanceRecorder := httptest.NewRecorder()
	router.ServeHTTP(wrongInstanceRecorder, httptest.NewRequest(http.MethodPost, envelope.Endpoint, bytes.NewReader(wrongInstanceBody)))
	if wrongInstanceRecorder.Code != http.StatusConflict {
		t.Fatalf("wrong instance status = %d body=%s", wrongInstanceRecorder.Code, wrongInstanceRecorder.Body.String())
	}
	assertClientPairingReason(t, wrongInstanceRecorder, "pairing_binding_mismatch")

	wrongFingerprintBody, _ := json.Marshal(clientPairingRedeemRequest{
		Version: "1", InstanceID: envelope.InstanceID,
		TLSFingerprintSHA256: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		OneTimeCode:          envelope.OneTimeCode,
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, envelope.Endpoint, bytes.NewReader(wrongFingerprintBody)))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("wrong fingerprint status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	assertClientPairingReason(t, recorder, "pairing_binding_mismatch")

	validBody, _ := json.Marshal(clientPairingRedeemRequest{
		Version: "1", InstanceID: envelope.InstanceID,
		TLSFingerprintSHA256: envelope.TLSFingerprintSHA256, OneTimeCode: envelope.OneTimeCode,
	})
	validRecorder := httptest.NewRecorder()
	router.ServeHTTP(validRecorder, httptest.NewRequest(http.MethodPost, envelope.Endpoint, bytes.NewReader(validBody)))
	if validRecorder.Code != http.StatusOK {
		t.Fatalf("valid redeem after mismatch = %d body=%s", validRecorder.Code, validRecorder.Body.String())
	}
}

func TestClientPairingRedeemRejectsExpiredCode(t *testing.T) {
	now := clientPairingTestNow
	config := clientPairingTestConfig(clientDeploymentModeSelfHosted)
	config.Clock = func() time.Time { return now }
	router := httpx.NewRouter()
	RegisterClientPairingRoutes(router, config)
	envelope := issueClientPairingForRouteTest(t, router)
	now = envelope.ExpiresAt

	body, _ := json.Marshal(clientPairingRedeemRequest{
		Version: "1", InstanceID: envelope.InstanceID,
		TLSFingerprintSHA256: envelope.TLSFingerprintSHA256, OneTimeCode: envelope.OneTimeCode,
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, envelope.Endpoint, bytes.NewReader(body)))
	if recorder.Code != http.StatusGone {
		t.Fatalf("expired status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	assertClientPairingReason(t, recorder, "pairing_code_expired")
}

func TestClientPairingLocalModeFailsClosedWithoutDuplicatingOwner(t *testing.T) {
	router := httpx.NewRouter()
	RegisterClientPairingRoutes(router, clientPairingTestConfig(clientDeploymentModeLocal))
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:5260"+clientPairingIssuePath, nil)
	req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{
		UserID: "breakglass:local-owner", OrgID: clientPairingTestTenant, Roles: []string{"admin"},
	}))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("local issue status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	assertClientPairingReason(t, recorder, "local_multiuser_not_supported")
}

func TestClientPairingIssueRequiresAdminAndConfiguredTenantBinding(t *testing.T) {
	config := clientPairingTestConfig(clientDeploymentModeSelfHosted)
	tests := []struct {
		name       string
		identity   *identity.Identity
		wantStatus int
		wantReason string
	}{
		{name: "unauthenticated", wantStatus: http.StatusUnauthorized, wantReason: "authentication_required"},
		{name: "member", identity: &identity.Identity{UserID: "member-1", OrgID: clientPairingTestTenant, Roles: []string{"member"}}, wantStatus: http.StatusForbidden, wantReason: "admin_role_required"},
		{name: "wrong tenant", identity: &identity.Identity{UserID: "owner-2", OrgID: "other-tenant", Roles: []string{"admin"}}, wantStatus: http.StatusForbidden, wantReason: "tenant_binding_mismatch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := httpx.NewRouter()
			RegisterClientPairingRoutes(router, config)
			req := httptest.NewRequest(http.MethodPost, "https://techstack.home.example"+clientPairingIssuePath, nil)
			if test.identity != nil {
				req = req.WithContext(identity.NewContext(context.Background(), test.identity))
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
			}
			assertClientPairingReason(t, recorder, test.wantReason)
		})
	}
}

func TestClientPairingRedeemRejectsUnknownFields(t *testing.T) {
	router := httpx.NewRouter()
	RegisterClientPairingRoutes(router, clientPairingTestConfig(clientDeploymentModeSelfHosted))
	body := `{"version":"1","instance_id":"` + clientDiscoveryTestInstanceID + `","tls_fingerprint_sha256":"` + clientPairingTestFingerprint + `","one_time_code":"abcdefghijklmnop","session_token":"forbidden"}`
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "https://techstack.home.example"+clientpairing.RedeemPath, bytes.NewBufferString(body)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	assertClientPairingReason(t, recorder, "invalid_pairing_request")
}

func clientPairingTestConfig(mode string) ClientPairingRouteConfig {
	profile := ClientConnectionProfileConfig{
		DeploymentMode: mode,
		BaseURL:        "https://techstack.home.example",
		InstanceID:     clientDiscoveryTestInstanceID,
		OIDCIssuer:     "https://id.home.example/oidc",
		OIDCClientID:   "techstack-native-public",
		OIDCAudience:   "https://techstack.home.example/api",
		OIDCScopes:     []string{"openid", "profile", "offline_access"},
		OIDCFlow:       clientFlowPKCE,
	}
	if mode == clientDeploymentModeLocal {
		profile.BaseURL = ""
	}
	return ClientPairingRouteConfig{
		Profile: profile, Store: clientpairing.NewMemoryStore(),
		DefaultTenantID: clientPairingTestTenant, TLSFingerprintSHA256: clientPairingTestFingerprint,
		Clock: func() time.Time { return clientPairingTestNow },
	}
}

func issueClientPairingForRouteTest(t *testing.T, router *httpx.Router) clientpairing.Envelope {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "https://techstack.home.example"+clientPairingIssuePath, nil)
	req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{
		UserID: "owner-1", OrgID: clientPairingTestTenant, Roles: []string{"admin"},
	}))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("issue status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope clientpairing.Envelope
	if err := json.NewDecoder(recorder.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func assertClientPairingReason(t *testing.T, recorder *httptest.ResponseRecorder, want string) {
	t.Helper()
	var envelope clientPairingErrorEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.ReasonCode != want || envelope.UserGuidance.Title == "" || len(envelope.UserGuidance.NextSteps) == 0 {
		t.Fatalf("error envelope = %#v", envelope)
	}
}
