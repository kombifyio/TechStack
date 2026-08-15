package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func TestWorkerEnrollmentResponseMarksExplicitPrivateLANHTTP(t *testing.T) {
	event := &httpx.Event{Request: httptest.NewRequest(http.MethodPost, "http://192.168.10.2:5264/api/v1/workers/register", nil), Response: httptest.NewRecorder()}
	event.Request.Header.Set("X-Techstack-LAN-Bridge", "1")
	response := (workerRouteHandlers{}).workerEnrollmentResponse(event, workerEnrollmentContext{WorkerID: "agent-1"})
	if response["allow_private_lan_http"] != true || response["private_lan_http_origin"] != "http://192.168.10.2:5264" || response["transport_security"] != "private-lan-http" {
		t.Fatalf("private-LAN enrollment capability missing: %#v", response)
	}
}
