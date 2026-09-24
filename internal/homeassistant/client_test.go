package homeassistant_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/homeassistant"
)

// Credential boundary: read-only adoption must never redirect bearer tokens or
// turn a successful Core read into Supervisor or mutation authority.
func TestObserveReadOnlyAndRefusesRedirect(t *testing.T) {
	redirected := false
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
	defer other.Close()
	redirect := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/config" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("unexpected authenticated read: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		if redirect {
			http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
			return
		}
		_, _ = w.Write([]byte(`{"version":"2026.9.1","components":["mcp_server"],"time_zone":"Europe/Berlin"}`))
	}))
	defer server.Close()
	c, err := homeassistant.NewLocalClient(context.Background(), server.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	o, err := c.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if o.CoreVersion != "2026.9.1" || !o.Capabilities["mcp_integration_loaded"] || o.Capabilities["supervisor_authorized"] || o.Capabilities["backup_restore_authorized"] {
		t.Fatalf("incorrect measured capabilities: %+v", o)
	}
	redirect = true
	if _, err = c.Observe(context.Background()); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("redirect must fail without credential disclosure: %v", err)
	}
	if redirected {
		t.Fatal("followed redirect with credential")
	}
}
