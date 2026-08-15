package kombifyme

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientForward_DirectUsesKombifyMeAPIKeyForReadOnlyRegistryAccess(t *testing.T) {
	var received struct {
		Authorization string
		APIKey        string
		Path          string
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Authorization = r.Header.Get("Authorization")
		received.APIKey = r.Header.Get("X-Kombify-API-Key")
		received.Path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"sub-1"}]`))
	}))
	defer server.Close()

	client := NewClient(Config{
		DirectAPIBase: server.URL + "/_kombify/api/v1",
		APIKey:        "kbi_test",
	})

	resp, err := client.Forward(context.Background(), http.MethodGet, "subdomains/", nil, "Bearer cloud-token")
	if err != nil {
		t.Fatalf("Forward failed: %v", err)
	}

	if resp.Status != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Status, http.StatusOK)
	}
	if received.Authorization != "" {
		t.Fatalf("direct mode should not forward incoming Authorization, got %q", received.Authorization)
	}
	if received.APIKey != "kbi_test" {
		t.Fatalf("api key = %q, want kbi_test", received.APIKey)
	}
	if received.Path != "/_kombify/api/v1/subdomains/" {
		t.Fatalf("path = %q", received.Path)
	}
}

func TestClientForward_CloudUsesIncomingAuthorization(t *testing.T) {
	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"sub-1"}]`))
	}))
	defer server.Close()

	client := NewClient(Config{CloudAPIBase: server.URL + "/api/v1/kombify-me"})
	_, err := client.Forward(context.Background(), http.MethodGet, "subdomains/", nil, "Bearer tool.jwt")
	if err != nil {
		t.Fatalf("Forward failed: %v", err)
	}
	if receivedAuth != "Bearer tool.jwt" {
		t.Fatalf("Authorization = %q, want incoming token", receivedAuth)
	}
}

func TestClientForward_RetriesRateLimitedRegistryReads(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "application/json")
		if attempts == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate_limited"}`))
			return
		}
		_, _ = w.Write([]byte(`[{"id":"sub-1","name":"demo"}]`))
	}))
	defer server.Close()

	client := NewClient(Config{
		DirectAPIBase: server.URL + "/_kombify/api/v1",
		APIKey:        "kbi_test",
	})
	resp, err := client.Forward(context.Background(), http.MethodGet, "subdomains/", nil, "")
	if err != nil {
		t.Fatalf("Forward failed: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.Status, http.StatusOK)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestDefaultConfigFromEnv_UsesLegacyKombifyAPIKey(t *testing.T) {
	t.Setenv("KOMBIFY_ME_API_KEY", "")
	t.Setenv("KOMBIFY_API_KEY", "kbi_legacy")

	cfg := DefaultConfigFromEnv()
	if cfg.APIKey != "kbi_legacy" {
		t.Fatalf("APIKey = %q, want legacy KOMBIFY_API_KEY fallback", cfg.APIKey)
	}
}

func TestClientForward_DoesNotRewriteDeleteCalls(t *testing.T) {
	var receivedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := NewClient(Config{
		DirectAPIBase: server.URL + "/_kombify/api/v1",
		APIKey:        "kbi_test",
	})

	_, err := client.Forward(
		context.Background(),
		http.MethodDelete,
		"subdomains/base-1",
		map[string]any{"reason": "legacy-cleanup"},
		"",
	)
	if err != nil {
		t.Fatalf("Forward failed: %v", err)
	}

	if _, ok := receivedBody["kind"]; ok {
		t.Fatal("delete payload should not receive registration kind")
	}
	if _, ok := receivedBody["device_fingerprint"]; ok {
		t.Fatal("delete payload should not receive registration fingerprint")
	}
	if receivedBody["reason"] != "legacy-cleanup" {
		t.Fatalf("reason = %v, want legacy-cleanup", receivedBody["reason"])
	}
}
