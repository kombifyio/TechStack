package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	commonauthlocal "github.com/kombifyio/techstack/internal/gocommon/authlocal"
	"github.com/kombifyio/techstack/internal/gocommon/authsession"
	"github.com/kombifyio/techstack/pkg/auth/sessionpolicy"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/logger"
)

// One public HTTP boundary proves a captured request cannot mint a second
// admin session or cross the configured loopback origin.
func TestLocalDeviceProofBindsOriginAndConsumesChallenge(t *testing.T) {
	secret := strings.Repeat("a", 64)
	t.Setenv(localDeviceTokenEnv, secret)
	t.Setenv("TECHSTACK_PUBLIC_ORIGIN", "http://127.0.0.1:5260")
	localDeviceSessionAttempts.reset()
	t.Cleanup(localDeviceSessionAttempts.reset)
	cfg := config.DefaultConfig()
	cfg.Server.Environment = "local"
	sessionConfig := sessionpolicy.BrowserSessionConfig("techstack-local", []byte(strings.Repeat("s", 32)))
	manager, err := authsession.NewManager(sessionConfig)
	if err != nil {
		t.Fatal(err)
	}
	browser, err := sessionpolicy.NewBrowser(sessionConfig, manager, "techstack_session", false)
	if err != nil {
		t.Fatal(err)
	}
	router := httpx.NewRouter()
	deps := routeDeps{
		startup: &startupContext{cfg: cfg},
		v2: &v2Boot{session: manager, browserSessions: browser,
			cookieName: "techstack_session", defaultTenant: "default"},
		log: logger.Default(),
	}
	bindGlobalMiddleware(router, deps)
	registerLocalDeviceSessionRouteWithStore(router, deps, &memoryBreakglassStore{rec: &commonauthlocal.Record{
		Email: "owner@techstack.local", Claimed: true,
	}})
	router.GET("/test/device-admin", func(e *httpx.Event) error {
		id := identity.FromContext(e.Request.Context())
		if id == nil || !id.IsAuthenticated() || !slices.Contains(id.Roles, "admin") {
			return httpx.Forbidden(e, "device admin session required")
		}
		return httpx.Success(e, http.StatusOK, map[string]any{"ok": true})
	})
	handler := router.BuildMux()
	clientNonce := strings.Repeat("b", 32)
	request := func(path, body, proof string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:5260"+path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:52100"
		if proof != "" {
			req.Header.Set(localDeviceProofHeader, proof)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	challenge := request(localDeviceChallengePath,
		`{"origin":"http://127.0.0.1:5260","client_nonce":"`+clientNonce+`"}`, "")
	if challenge.Code != http.StatusOK {
		t.Fatalf("challenge status = %d: %s", challenge.Code, challenge.Body.String())
	}
	var envelope struct {
		Data struct {
			ServerNonce string `json:"server_nonce"`
			Proof       string `json:"proof"`
		} `json:"data"`
	}
	if err := json.Unmarshal(challenge.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !localDeviceMACMatches(envelope.Data.Proof,
		localDeviceMAC(secret, localDeviceProofMessage("server", "http://127.0.0.1:5260", clientNonce, envelope.Data.ServerNonce))) {
		t.Fatal("server challenge did not prove the configured origin")
	}
	body := `{"origin":"http://127.0.0.1:5260","client_nonce":"` + clientNonce + `","server_nonce":"` + envelope.Data.ServerNonce + `"}`
	proof := localDeviceMAC(secret, localDeviceProofMessage("client", "http://127.0.0.1:5260",
		clientNonce, envelope.Data.ServerNonce, http.MethodPost, localDeviceProofPath))
	first := request(localDeviceProofPath, body, proof)
	if first.Code != http.StatusOK || len(first.Result().Cookies()) != 2 {
		t.Fatalf("first proof failed: status=%d cookies=%d", first.Code, len(first.Result().Cookies()))
	}
	wantResponseProof := localDeviceMAC(secret, localDeviceProofMessage("response", "http://127.0.0.1:5260",
		clientNonce, envelope.Data.ServerNonce, "200", localDeviceCanonicalCookies(first.Header().Values("Set-Cookie"))))
	if !localDeviceMACMatches(first.Header().Get(localDeviceProofHeader), wantResponseProof) {
		t.Fatal("issued session cookies were not bound to the response proof")
	}
	protected := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5260/test/device-admin", nil)
	protected.RemoteAddr = "127.0.0.1:52100"
	for _, cookie := range first.Result().Cookies() {
		protected.AddCookie(cookie)
	}
	if _, err := browser.VerifyTransfer(protected); err != nil {
		t.Fatalf("stamped session pair is not a valid browser transfer: %v", err)
	}
	protectedResponse := httptest.NewRecorder()
	handler.ServeHTTP(protectedResponse, protected)
	if protectedResponse.Code != http.StatusOK {
		t.Fatalf("stamped session pair did not authorize the admin route: %d", protectedResponse.Code)
	}
	replay := request(localDeviceProofPath, body, proof)
	if replay.Code != http.StatusUnauthorized || len(replay.Result().Cookies()) != 0 {
		t.Fatalf("replayed proof minted a session: status=%d", replay.Code)
	}
	wrongOrigin := request(localDeviceChallengePath,
		`{"origin":"http://localhost:5260","client_nonce":"`+clientNonce+`"}`, "")
	if wrongOrigin.Code != http.StatusBadRequest || len(wrongOrigin.Result().Cookies()) != 0 {
		t.Fatalf("different loopback origin admitted: status=%d", wrongOrigin.Code)
	}
}
