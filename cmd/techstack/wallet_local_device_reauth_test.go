package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/authlocal"
	"github.com/kombifyio/techstack/internal/gocommon/authsession"
	"github.com/kombifyio/techstack/internal/routes"
	"github.com/kombifyio/techstack/pkg/auth/sessionpolicy"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/logger"
)

// Sensitive boundary: only the confirmed current device/session/item snapshot
// may disclose, once. The installed native transport consumes this same API.
func TestWindowsLocalWalletDeviceRevealBoundary(t *testing.T) {
	const origin = "http://127.0.0.1:5260"
	const device = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const secret = "synthetic-device-wallet-value"
	t.Setenv(localDeviceTokenEnv, device)
	t.Setenv("TECHSTACK_PUBLIC_ORIGIN", origin)
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
	ownerStore := &memoryBreakglassStore{}
	local, err := authlocal.New(authlocal.Config{Store: ownerStore, Sessions: manager, DefaultTenantID: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := local.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	deps := routeDeps{startup: &startupContext{cfg: cfg}, log: logger.Default(),
		v2: &v2Boot{session: manager, browserSessions: browser, cookieName: "techstack_session", defaultTenant: "default"}}
	store := controlplane.NewMemoryStore()
	authority := &walletDeviceAuthority{deps: deps, store: ownerStore, now: time.Now, pending: make(map[string]routes.WalletDeviceChallenge)}
	newHandler := func() http.Handler {
		router := httpx.NewRouter()
		bindGlobalMiddleware(router, deps)
		registerLocalDeviceSessionRouteWithStore(router, deps, ownerStore)
		routes.RegisterWalletRoutesWithConfig(router, routes.WalletRouteConfig{Store: store, Activity: store,
			DeviceReauth: &routes.WalletLocalDeviceReauth{Challenge: authority.challenge, Verify: authority.verify}})
		return router.BuildMux()
	}
	handler := newHandler()
	request := func(path string, body any, cookies []*http.Cookie, csrf bool) *httptest.ResponseRecorder {
		encoded, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, origin+path, strings.NewReader(string(encoded)))
		req.RemoteAddr = "127.0.0.1:51000"
		req.Header.Set("Content-Type", "application/json")
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		if path == localDeviceSessionPath {
			req.Header.Set(localDeviceSessionHeader, device)
		}
		if csrf {
			req.AddCookie(&http.Cookie{Name: "csrf_token", Value: strings.Repeat("c", 64)})
			req.Header.Set("X-CSRF-Token", strings.Repeat("c", 64))
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	sessionResponse := request(localDeviceSessionPath, nil, nil, false)
	if sessionResponse.Code != http.StatusOK {
		t.Fatalf("device session HTTP %d", sessionResponse.Code)
	}
	cookies := sessionResponse.Result().Cookies()
	probe := httptest.NewRequest(http.MethodGet, origin, nil)
	for _, cookie := range cookies {
		probe.AddCookie(cookie)
	}
	transfer, err := browser.VerifyTransfer(probe)
	if err != nil {
		t.Fatal(err)
	}
	owner := transfer.Claims.Subject
	item, err := store.CreateWalletItem(context.Background(), controlplane.WalletItem{ID: "29ef1f3a-9d4f-4ca0-94bc-87ab809a04f4:pocketid:owner",
		TenantID: "default", ItemType: "password", Metadata: map[string]any{"owner_id": owner, "name": "Synthetic device credential", "secret": secret, "revealable": true}})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateWalletItem(context.Background(), controlplane.WalletItem{ID: "wallet_4516059894ce4dfebec71a69",
		TenantID: "default", ItemType: "password", Metadata: map[string]any{"owner_id": owner, "secret": "other-synthetic-value", "revealable": true}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/wallet/" + item.ID
	for _, scenario := range []string{"confirmed", "replay", "bad MAC consumes", "expiry", "wrong item", "renamed snapshot", "rotated ciphertext", "changed cookie pair", "missing anchor", "duplicate cookie", "canonical claim changed", "wrong provider", "wrong owner", "wrong tenant", "missing CSRF"} {
		t.Run(scenario, func(t *testing.T) {
			// Each independent scenario retains the production rate-limiter budget.
			handler = newHandler()
			authority.now = time.Now
			ownerStore.rec.Claimed = false
			response := request(path+"/device-reauth/challenge", map[string]string{"reason": scenario, "client_nonce": strings.Repeat("b", 32)}, cookies, true)
			if response.Code != http.StatusOK {
				t.Fatalf("challenge HTTP %d", response.Code)
			}
			var envelope struct {
				Data routes.WalletDeviceChallenge `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			c := envelope.Data
			if !localDeviceMACMatches(c.Proof, localDeviceMAC(device, walletDeviceTranscript(c, "challenge"))) {
				t.Fatal("runtime challenge not authenticated")
			}
			proof := routes.WalletDeviceProof{ClientNonce: c.ClientNonce, ServerNonce: c.ServerNonce,
				Proof: localDeviceMAC(device, walletDeviceTranscript(c, "reveal", http.MethodPost, path+"/reveal"))}
			requestCookies := append([]*http.Cookie{}, cookies...)
			revealPath := path + "/reveal"
			withCSRF := true
			switch scenario {
			case "expiry":
				authority.now = func() time.Time { return time.Now().Add(3 * time.Minute) }
			case "wrong item":
				revealPath = "/api/v1/wallet/" + other.ID + "/reveal"
			case "renamed snapshot", "rotated ciphertext":
				changed, err := store.GetWalletItem(context.Background(), "default", item.ID)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "renamed snapshot" {
					changed.Metadata["name"] = "Changed confirmation"
				} else {
					changed.Metadata["secret"] = "rotated-value"
				}
				if _, err := store.UpdateWalletItem(context.Background(), *changed); err != nil {
					t.Fatal(err)
				}
			case "changed cookie pair", "wrong provider", "wrong owner", "wrong tenant":
				claims := transfer.Claims
				switch scenario {
				case "changed cookie pair":
					claims.Role = "admin,developer"
				case "wrong provider":
					claims.Provider = "cloud"
				case "wrong owner":
					claims.Subject = "foreign-device"
				case "wrong tenant":
					claims.TenantID = "foreign-tenant"
				}
				token, err := manager.Issue(claims)
				if err != nil {
					t.Fatal(err)
				}
				issued := httptest.NewRecorder()
				authsession.SetSessionCookie(issued, "techstack_session", token, false)
				browser.StampLogin(issued.Header())
				requestCookies = issued.Result().Cookies()
			case "missing anchor":
				requestCookies = requestCookies[:1]
			case "duplicate cookie":
				requestCookies = append(requestCookies, requestCookies[0])
			case "canonical claim changed":
				ownerStore.rec.Claimed = true
			case "missing CSRF":
				withCSRF = false
			case "bad MAC consumes":
				proof.Proof = strings.Repeat("0", 64)
			}
			body := map[string]any{"reason": scenario, "device_proof": proof}
			result := request(revealPath, body, requestCookies, withCSRF)
			allow := scenario == "confirmed" || scenario == "replay"
			if allow {
				if result.Code != http.StatusOK {
					t.Fatalf("confirmed reveal HTTP %d", result.Code)
				}
				var data struct {
					Data struct{ Payload, Proof string } `json:"data"`
				}
				if err := json.Unmarshal(result.Body.Bytes(), &data); err != nil {
					t.Fatal(err)
				}
				if !localDeviceMACMatches(data.Data.Proof, localDeviceMAC(device, walletDeviceTranscript(c, "result", "200", data.Data.Payload))) {
					t.Fatal("result not authenticated")
				}
				decoded, err := base64.RawURLEncoding.DecodeString(data.Data.Payload)
				if err != nil {
					t.Fatal(err)
				}
				var value struct {
					Secret string `json:"secret"`
				}
				if err := json.Unmarshal(decoded, &value); err != nil || value.Secret != secret {
					t.Fatal("confirmed disclosure did not recover the value")
				}
			} else if result.Code == http.StatusOK || strings.Contains(result.Body.String(), secret) {
				t.Fatalf("invalid confirmation disclosed: HTTP %d", result.Code)
			}
			if scenario == "replay" || scenario == "bad MAC consumes" {
				proof.Proof = localDeviceMAC(device, walletDeviceTranscript(c, "reveal", http.MethodPost, path+"/reveal"))
				replayed := request(path+"/reveal", map[string]any{"reason": scenario, "device_proof": proof}, cookies, true)
				if replayed.Code != http.StatusForbidden {
					t.Fatal("consumed proof could be replayed")
				}
			}
			activity, err := store.ListActivity(context.Background(), "default", "", 100)
			if err != nil {
				t.Fatal(err)
			}
			auditedDisclosure := false
			for _, event := range activity {
				if event.Action == "wallet_reveal" && event.Details["reason"] == scenario {
					auditedDisclosure = true
				}
			}
			if auditedDisclosure != allow {
				t.Fatal("disclosure audit did not match authorized effect")
			}
		})
	}
}
