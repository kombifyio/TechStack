package kombifyme

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLiveRelayProductionRoundTrip(t *testing.T) {
	if os.Getenv("KOMBIFY_ME_LIVE_E2E") != "1" {
		t.Skip("set KOMBIFY_ME_LIVE_E2E=1 to run the production relay proof")
	}
	apiKey := strings.TrimSpace(os.Getenv("KOMBIFY_ME_API_KEY"))
	if !strings.HasPrefix(apiKey, "kbi_") {
		t.Fatal("KOMBIFY_ME_API_KEY must contain an active kbi_ credential")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client := NewClient(Config{APIKey: apiKey})
	token := fmt.Sprintf("relay-%d", time.Now().UnixNano())
	var publicFQDN string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/probe" || r.URL.Query().Get("token") != token {
			t.Errorf("target request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token":          token,
			"forwarded_host": r.Header.Get("X-Forwarded-Host"),
		})
	}))
	defer target.Close()

	baseID, cleanupBase := createLiveRelayBase(t, ctx, client)
	defer cleanupBase()
	serviceName := fmt.Sprintf("relay%d", time.Now().Unix()%1_000_000_000)
	createdResponse, createErr := client.Forward(ctx, http.MethodPost, fmt.Sprintf("subdomains/%s/services", baseID), map[string]any{
		"service_name": serviceName,
		"local_addr":   target.URL,
		"target_type":  "tunnel",
		"routing_mode": "passthrough",
		"description":  "bounded production relay E2E",
	}, "")
	created := requireLiveRelayCall(t, createdResponse, createErr)
	routeID := requireLiveRelayString(t, created.Body, "id")
	ownerUserID := requireLiveRelayString(t, created.Body, "user_id")
	publicFQDN = requireLiveRelayString(t, created.Body, "fqdn")
	t.Log("created and exposed a temporary owner tunnel route")
	deletePath := fmt.Sprintf("subdomains/%s/services/%s", baseID, routeID)
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := client.Forward(cleanupCtx, http.MethodDelete, deletePath, nil, ""); err != nil {
			t.Errorf("delete live relay route: %v", err)
		}
	}()

	exposeResponse, exposeErr := client.Forward(ctx, http.MethodPut, deletePath+"/expose", map[string]bool{"exposed": true}, "")
	requireLiveRelayCall(t, exposeResponse, exposeErr)
	ownerResponse, ownerErr := client.Forward(ctx, http.MethodGet, "subdomains/"+routeID, nil, "")
	requireLiveRelayCall(t, ownerResponse, ownerErr)
	isolationURL, cleanupIsolation := createLiveRelayIsolationRoute(t, ctx, ownerUserID, target.URL, token)
	defer cleanupIsolation()
	t.Log("created a temporary cross-owner isolation route")

	publicURL := "https://" + publicFQDN + "/probe?token=" + token
	unavailable := requireLiveRelayStatus(t, ctx, publicURL, http.StatusServiceUnavailable, nil)
	if unavailable.Header.Get("Retry-After") != "5" {
		t.Fatalf("unavailable Retry-After = %q", unavailable.Header.Get("Retry-After"))
	}
	requireLiveRelayStatus(t, ctx, isolationURL, http.StatusServiceUnavailable, nil)
	t.Log("verified both routes are unavailable before the relay connects")

	relay, err := NewRelay(RelayConfig{APIKey: apiKey, AgentID: token, Version: "live-e2e"})
	if err != nil {
		t.Fatal(err)
	}
	stopFirst, firstErr := startLiveRelaySession(ctx, relay)
	connected := requireLiveRelayStatus(t, ctx, publicURL, http.StatusOK, firstErr)
	requireLiveRelayBody(t, connected.Body, token, publicFQDN)
	requireLiveRelayStatus(t, ctx, isolationURL, http.StatusServiceUnavailable, nil)
	t.Log("verified private target forwarding while the cross-owner route stayed unavailable")
	stopLiveRelaySession(t, stopFirst, firstErr)
	requireLiveRelayStatus(t, ctx, publicURL, http.StatusServiceUnavailable, nil)
	t.Log("verified disconnect returns the owner route to unavailable")

	stopSecond, secondErr := startLiveRelaySession(ctx, relay)
	reconnected := requireLiveRelayStatus(t, ctx, publicURL, http.StatusOK, secondErr)
	requireLiveRelayBody(t, reconnected.Body, token, publicFQDN)
	requireLiveRelayStatus(t, ctx, isolationURL, http.StatusServiceUnavailable, nil)
	t.Log("verified reconnect restores forwarding without weakening owner isolation")
	stopLiveRelaySession(t, stopSecond, secondErr)
}

type liveRelayHTTPResult struct {
	Status int
	Header http.Header
	Body   []byte
}

func createLiveRelayBase(t *testing.T, ctx context.Context, client *Client) (string, func()) {
	t.Helper()
	profileResponse, profileErr := client.Forward(ctx, http.MethodGet, "auth/me", nil, "")
	profile := requireLiveRelayCall(t, profileResponse, profileErr)
	fingerprint := requireLiveRelayString(t, profile.Body, "device_fingerprint")
	homelabName := fmt.Sprintf("relaye2e%d", time.Now().Unix()%1_000_000_000)
	createdResponse, createErr := client.Forward(ctx, http.MethodPost, "subdomains/auto-register", map[string]any{
		"homelab_name":       homelabName,
		"kind":               "self-hosted",
		"device_fingerprint": fingerprint,
		"description":        "bounded production relay E2E base",
	}, "")
	created := requireLiveRelayCall(t, createdResponse, createErr)
	baseID := requireLiveRelayString(t, created.Body, "id")
	cleanup := func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := client.Forward(cleanupCtx, http.MethodDelete, "subdomains/"+baseID, nil, ""); err != nil {
			t.Errorf("delete live relay base route: %v", err)
		}
	}
	return baseID, cleanup
}

func requireLiveRelayCall(t *testing.T, response *UpstreamResponse, err error) *UpstreamResponse {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func requireLiveRelayString(t *testing.T, body any, field string) string {
	t.Helper()
	object, ok := body.(map[string]any)
	if !ok {
		t.Fatalf("response body type = %T", body)
	}
	value, ok := object[field].(string)
	if !ok || value == "" {
		t.Fatalf("response field %s is missing", field)
	}
	return value
}

func createLiveRelayIsolationRoute(t *testing.T, ctx context.Context, ownerUserID, targetAddr, token string) (string, func()) {
	t.Helper()
	adminKey := strings.TrimSpace(os.Getenv("KOMBIFY_ME_ADMIN_KEY"))
	if adminKey == "" {
		t.Fatal("KOMBIFY_ME_ADMIN_KEY is required for the cross-owner production proof")
	}
	users := requireLiveRelayAdminJSON(t, ctx, adminKey, http.MethodGet, "users", nil, http.StatusOK)
	items, ok := users["items"].([]any)
	if !ok {
		t.Fatalf("admin users response type = %T", users["items"])
	}
	otherUserID := ""
	for _, item := range items {
		object, itemOK := item.(map[string]any)
		candidate, idOK := object["user_id"].(string)
		if itemOK && idOK && candidate != "" && candidate != ownerUserID {
			otherUserID = candidate
			break
		}
	}
	if otherUserID == "" {
		t.Fatal("cross-owner production proof requires a second existing route owner")
	}
	name := fmt.Sprintf("relayiso%d", time.Now().Unix()%1_000_000_000)
	created := requireLiveRelayAdminJSON(t, ctx, adminKey, http.MethodPost, "subdomains", map[string]any{
		"name":            name,
		"user_id":         otherUserID,
		"target_type":     "tunnel",
		"target_addr":     targetAddr,
		"routing_mode":    "passthrough",
		"status":          "active",
		"subdomain_kind":  "base",
		"exposed":         true,
		"rewrite_headers": true,
		"description":     "bounded cross-owner relay E2E",
	}, http.StatusCreated)
	subdomain, ok := created["subdomain"].(map[string]any)
	if !ok {
		t.Fatalf("admin subdomain response type = %T", created["subdomain"])
	}
	routeID := requireLiveRelayString(t, subdomain, "id")
	fqdn := requireLiveRelayString(t, subdomain, "fqdn")
	cleanup := func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := liveRelayAdminJSON(cleanupCtx, adminKey, http.MethodDelete, "subdomains/"+routeID, nil, http.StatusNoContent); err != nil {
			t.Errorf("delete cross-owner live relay route: %v", err)
		}
	}
	return "https://" + fqdn + "/probe?token=" + token, cleanup
}

func requireLiveRelayAdminJSON(t *testing.T, ctx context.Context, adminKey, method, path string, payload any, wantStatus int) map[string]any {
	t.Helper()
	decoded, err := liveRelayAdminJSON(ctx, adminKey, method, path, payload, wantStatus)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func liveRelayAdminJSON(ctx context.Context, adminKey, method, path string, payload any, wantStatus int) (map[string]any, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	// The live test fixes scheme and host; path values are test constants or API-returned UUIDs.
	req, err := http.NewRequestWithContext(ctx, method, "https://kombify.me/_kombify/api/admin/"+strings.TrimLeft(path, "/"), body) //nolint:gosec
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Kombify-Admin-Key", adminKey)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(req) //nolint:gosec // Fixed production test origin; see request construction above.
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != wantStatus {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("admin %s %s returned %d: %s", method, path, response.StatusCode, strings.TrimSpace(string(data)))
	}
	if wantStatus == http.StatusNoContent {
		return nil, nil
	}
	var decoded map[string]any
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func startLiveRelaySession(parent context.Context, relay *Relay) (context.CancelFunc, <-chan error) {
	ctx, cancel := context.WithCancel(parent)
	errCh := make(chan error, 1)
	go func() { errCh <- relay.RunSession(ctx) }()
	return cancel, errCh
}

func stopLiveRelaySession(t *testing.T, cancel context.CancelFunc, errCh <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("relay session shutdown error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("relay session did not stop")
	}
}

func requireLiveRelayStatus(t *testing.T, ctx context.Context, target string, want int, sessionErr <-chan error) liveRelayHTTPResult {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var last liveRelayHTTPResult
	for {
		if sessionErr != nil {
			select {
			case err := <-sessionErr:
				t.Fatalf("relay session ended before HTTP %d: %v", want, err)
			default:
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(req)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			last = liveRelayHTTPResult{Status: response.StatusCode, Header: response.Header.Clone(), Body: body}
			if response.StatusCode == want {
				return last
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for HTTP %d ended after status %d: %v", want, last.Status, ctx.Err())
		case <-ticker.C:
		}
	}
}

func requireLiveRelayBody(t *testing.T, response []byte, token, fqdn string) {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(response, &body); err != nil {
		t.Fatalf("decode live relay response: %v", err)
	}
	if body["token"] != token || body["forwarded_host"] != fqdn {
		t.Fatalf("live relay response identity mismatch: %#v", body)
	}
}
