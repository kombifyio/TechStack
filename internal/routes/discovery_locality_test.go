package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/httpx"
)

// Sensitive network boundary: SaaS may not scan its own network on behalf of
// a customer whose LAN executor has not been connected.
func TestDiscoveryWithoutLANExecutorDoesNotStartScan(t *testing.T) {
	disc := &fakeDiscovery{}
	handler := NewDiscoveryHandler(disc)
	handler.allowLAN = false
	recorder := httptest.NewRecorder()
	request := authenticatedDiscoveryTestRequest(http.MethodPost, "/api/v1/discovery/scan", "owner-1", nil, strings.NewReader(`{}`))
	_ = handler.startScan(&httpx.Event{Request: request, Response: recorder})
	if recorder.Code != http.StatusConflict || disc.startCtx != nil {
		t.Fatalf("scan crossed executor boundary: HTTP %d, started=%v", recorder.Code, disc.startCtx != nil)
	}
}
