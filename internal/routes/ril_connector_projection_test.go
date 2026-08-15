package routes

import (
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/pkg/httpx"
)

func TestConnectorProjectionRejectsUnverifiedRequestHeaders(t *testing.T) {
	request := httptest.NewRequest("POST", "/v1/ril/action-cards/card-1/execute", nil)
	request.Header.Set("X-Kombify-Connector-Grant-ID", "grant-forged")
	request.Header.Set("X-Kombify-Connector-Binding-ID", "binding-forged")
	event := &httpx.Event{Request: request, Response: httptest.NewRecorder()}

	if projection := connectorProjectionFromEvent(event); projection != nil {
		t.Fatalf("unverified connector projection accepted: %+v", projection)
	}
}
