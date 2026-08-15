package signals

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/go-common/servicecall"
)

func TestGatewayPublisherBindsServiceAndEndUserIdentity(t *testing.T) {
	const secret = "gateway-publisher-test-secret"
	envelope := Envelope{
		SignalID: "signal-1", ActionCardID: "ril-action-card:signal-1",
		TenantID: "tenant-1", UserID: "auth0|user-1", ServerID: "server-1",
		Source: SourceHealth, Severity: SeverityHigh, Priority: PriorityHigh,
		ReceivedAt: "2026-08-03T12:00:00Z", TraceID: "trace-1", AuditID: "audit-1",
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, err := servicecall.VerifyToken(request.Header.Get(servicecall.HeaderServiceAuth), secret, "")
		if err != nil {
			t.Errorf("verify service token: %v", err)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if claims.Svc != "techstack" || claims.Aud != "kombify-gateway" || claims.OnBehalfOf == nil ||
			claims.OnBehalfOf.Sub != envelope.UserID || claims.OnBehalfOf.OrgID != envelope.TenantID {
			t.Errorf("service claims = %+v", claims)
		}
		var got Envelope
		if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
			t.Errorf("decode envelope: %v", err)
		}
		if got.SignalID != envelope.SignalID || got.ActionCardID != envelope.ActionCardID {
			t.Errorf("envelope identity = %+v", got)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"ok": true, "signalId": got.SignalID,
			"actionCard": map[string]any{"id": got.ActionCardID},
		})
	}))
	defer server.Close()
	publisher, err := NewGatewayPublisher(server.URL, secret, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayPublisherRejectsReceiptIdentityDrift(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"ok": true, "signalId": "other", "actionCard": map[string]any{"id": "other"},
		})
	}))
	defer server.Close()
	publisher, err := NewGatewayPublisher(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	err = publisher.Publish(context.Background(), Envelope{
		SignalID: "signal-1", ActionCardID: "ril-action-card:signal-1",
		TenantID: "tenant-1", UserID: "user-1",
	})
	if err == nil {
		t.Fatal("identity drift must fail")
	}
}
