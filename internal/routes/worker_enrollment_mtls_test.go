package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/httpx"
)

type recordingWorkerAgentIdentityIssuer struct {
	request grpcserver.IssueRequest
}

func (issuer *recordingWorkerAgentIdentityIssuer) IssueAgentIdentity(_ context.Context, req grpcserver.IssueRequest) (*grpcserver.IssuedIdentity, error) {
	issuer.request = req
	return &grpcserver.IssuedIdentity{
		AgentID:    req.AgentID,
		TenantID:   req.TenantID,
		CertPEM:    []byte("certificate"),
		KeyPEM:     []byte("private-key"),
		CACertPEM:  []byte("ca"),
		Serial:     "1234",
		ExpiresAt:  time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
		EnrolledAt: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
	}, nil
}

func TestWorkerEnrollmentResponseIncludesMTLSIdentity(t *testing.T) {
	expiresAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	event := &httpx.Event{
		Request:  httptest.NewRequest(http.MethodPost, "/api/v1/workers/register", nil),
		Response: httptest.NewRecorder(),
	}

	response := (workerRouteHandlers{}).workerEnrollmentResponse(event, workerEnrollmentContext{
		WorkerID: "agent-1",
		TenantID: "tenant-1",
		GRPCIdentity: &grpcserver.IssuedIdentity{
			AgentID:   "agent-1",
			TenantID:  "tenant-1",
			CertPEM:   []byte("certificate"),
			KeyPEM:    []byte("private-key"),
			CACertPEM: []byte("ca"),
			Serial:    "1234",
			ExpiresAt: expiresAt,
		},
	})

	identity, ok := response["grpc_mtls"].(map[string]any)
	if !ok {
		t.Fatalf("grpc_mtls = %#v", response["grpc_mtls"])
	}
	if identity["schema_version"] != "techstack.agent-mtls-enrollment/v1" ||
		identity["agent_id"] != "agent-1" || identity["tenant_id"] != "tenant-1" ||
		identity["certificate_pem"] != "certificate" || identity["private_key_pem"] != "private-key" ||
		identity["ca_pem"] != "ca" || identity["serial"] != "1234" ||
		identity["expires_at"] != "2026-09-01T12:00:00Z" {
		t.Fatalf("grpc_mtls = %#v", identity)
	}
}

func TestWorkerEnrollmentResponseOmitsMTLSIdentityWhenGRPCIsDisabled(t *testing.T) {
	response := (workerRouteHandlers{}).workerEnrollmentResponse(nil, workerEnrollmentContext{WorkerID: "agent-1"})
	if _, ok := response["grpc_mtls"]; ok {
		t.Fatalf("disabled gRPC enrollment leaked grpc_mtls: %#v", response)
	}
}

// Only a request the LAN bridge itself received names its origin and key pin;
// a client cannot claim the bridge with a header or Host value.
func TestWorkerEnrollmentResponseNamesOnlyTheBridgeItArrivedThrough(t *testing.T) {
	const origin, pin = "https://192.168.10.2:5264", "sha256//47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU="
	bridged := httptest.NewRequest(http.MethodPost, origin+"/api/v1/workers/register", nil)
	bridged = bridged.WithContext(WithLocalLANBridge(bridged.Context(), origin, pin))
	response := (workerRouteHandlers{}).workerEnrollmentResponse(&httpx.Event{Request: bridged, Response: httptest.NewRecorder()}, workerEnrollmentContext{WorkerID: "agent-1"})
	if response["private_lan_origin"] != origin || response["private_lan_pin"] != pin || response["heartbeat_url"] == nil {
		t.Fatalf("bridge enrollment lacks its origin and pin: %#v", response)
	}
	if heartbeat, _ := response["heartbeat_url"].(string); !strings.HasPrefix(heartbeat, origin+"/") {
		t.Fatalf("bridge enrollment points the Guard at %q, not the bridge", heartbeat)
	}

	forged := httptest.NewRequest(http.MethodPost, origin+"/api/v1/workers/register", nil)
	forged.Header.Set("X-Techstack-LAN-Bridge", "1")
	response = (workerRouteHandlers{}).workerEnrollmentResponse(&httpx.Event{Request: forged, Response: httptest.NewRecorder()}, workerEnrollmentContext{WorkerID: "agent-1"})
	if _, ok := response["private_lan_pin"]; ok {
		t.Fatalf("a request outside the bridge was given a LAN pin: %#v", response)
	}
}
