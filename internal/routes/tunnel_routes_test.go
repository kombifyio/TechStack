package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/tunnel"
)

func TestTunnelInstallCommandUsesPostBodyAndDoesNotReturnTokenField(t *testing.T) {
	resolver := tunnel.NewRegistryURLResolver(tunnel.Config{
		Mode:      tunnel.NetworkModeCustom,
		CustomURL: "https://registry.example.test",
		LocalPort: 5260,
		GRPCPort:  5263,
	}, logger.NewNop())
	event, recorder := tunnelInstallCommandTestEvent(http.MethodPost, "/api/v1/tunnel/install-command", `{"token":"test-token-123"}`)

	if err := tunnelInstallCommand(event, resolver); err != nil {
		t.Fatalf("tunnelInstallCommand returned unexpected error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := envelope.Data["token"]; ok {
		t.Fatalf("response must not expose a standalone token field: %s", recorder.Body.String())
	}
	if envelope.Data["registry_url"] != "https://registry.example.test" {
		t.Fatalf("registry_url = %q, want https://registry.example.test", envelope.Data["registry_url"])
	}
	if !strings.Contains(envelope.Data["linux"], "KOMBI_TOKEN='test-token-123'") {
		t.Fatalf("linux command did not contain body token: %q", envelope.Data["linux"])
	}
	if !strings.Contains(envelope.Data["windows"], "$env:KOMBI_TOKEN='test-token-123'") {
		t.Fatalf("windows command did not contain body token: %q", envelope.Data["windows"])
	}
}

func TestTunnelInstallCommandRejectsQueryTokenTransport(t *testing.T) {
	resolver := tunnel.NewRegistryURLResolver(tunnel.Config{
		Mode:      tunnel.NetworkModeCustom,
		CustomURL: "https://registry.example.test",
		LocalPort: 5260,
		GRPCPort:  5263,
	}, logger.NewNop())
	event, recorder := tunnelInstallCommandTestEvent(http.MethodGet, "/api/v1/tunnel/install-command?token=test-token-123", "")

	if err := tunnelInstallCommand(event, resolver); err != nil {
		t.Fatalf("tunnelInstallCommand returned unexpected error: %v", err)
	}
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d body=%s, want 405", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "test-token-123") {
		t.Fatalf("query token leaked into rejection response: %s", recorder.Body.String())
	}
}

func tunnelInstallCommandTestEvent(method, target, body string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{UserID: "operator"}))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}
