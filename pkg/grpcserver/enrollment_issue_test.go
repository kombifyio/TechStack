package grpcserver

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/auth"
)

func parseCertPEM(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("decode issued cert PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse issued cert: %v", err)
	}
	return cert
}

// TestIssueAgentIdentity_AcceptedByRegister is the end-to-end proof: an identity
// minted by IssueAgentIdentity passes the full Phase-7.1 Register binding
// (CN == agent_id, Organization == tenant, serial == pinned enrollment).
func TestIssueAgentIdentity_AcceptedByRegister(t *testing.T) {
	cm, err := auth.NewCertManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewCertManager: %v", err)
	}
	store := NewMemoryAgentEnrollmentStore()

	issued, err := IssueAgentIdentity(context.Background(), cm, store, IssueRequest{
		TenantID:              "tenant-A",
		AgentID:               "agent-1",
		AllowedCommandClasses: []string{"health_check", "get_logs"},
		ValidDays:             30,
		EnrolledBy:            "connect-flow",
	})
	if err != nil {
		t.Fatalf("IssueAgentIdentity: %v", err)
	}
	if len(issued.CACertPEM) == 0 {
		t.Error("issued identity missing CA cert")
	}

	// The enrollment must be pinned to the minted serial.
	enr, err := store.Get(context.Background(), "tenant-A", "agent-1")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if enr.CertSerial != issued.Serial {
		t.Errorf("pinned serial %q != issued serial %q", enr.CertSerial, issued.Serial)
	}
	if len(enr.AllowedCommandClasses) != 2 {
		t.Errorf("allowed classes = %v, want 2", enr.AllowedCommandClasses)
	}

	// Drive the real Register path with the issued cert.
	cert := parseCertPEM(t, issued.CertPEM)
	srv := newTestServerWithEnrollment(t, store)
	resp, err := srv.Register(ctxWithPeerCert(cert), &agentpb.RegisterRequest{
		AgentId:  "agent-1",
		Hostname: "host-1",
	})
	if err != nil {
		t.Fatalf("Register with issued cert: %v", err)
	}
	if !resp.Accepted {
		t.Fatal("Register did not accept the issued identity")
	}

	got, ok := srv.GetAgent("agent-1")
	if !ok {
		t.Fatal("agent missing from registry after Register")
	}
	if got.Tenant != "tenant-A" {
		t.Errorf("bound tenant = %q, want tenant-A", got.Tenant)
	}
	if got.CertSerial != issued.Serial {
		t.Errorf("bound serial = %q, want %q", got.CertSerial, issued.Serial)
	}
}

func TestIssueAgentIdentity_Validation(t *testing.T) {
	cm, err := auth.NewCertManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewCertManager: %v", err)
	}
	store := NewMemoryAgentEnrollmentStore()

	cases := []struct {
		name string
		cm   *auth.CertManager
		st   AgentEnrollmentStore
		req  IssueRequest
	}{
		{"nil cert manager", nil, store, IssueRequest{TenantID: "t", AgentID: "a"}},
		{"nil store", cm, nil, IssueRequest{TenantID: "t", AgentID: "a"}},
		{"empty tenant", cm, store, IssueRequest{AgentID: "a"}},
		{"empty agent", cm, store, IssueRequest{TenantID: "t"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := IssueAgentIdentity(context.Background(), tc.cm, tc.st, tc.req); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestServerIssueAgentIdentityUsesConfiguredAuthority(t *testing.T) {
	cm, err := auth.NewCertManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewCertManager: %v", err)
	}
	store := NewMemoryAgentEnrollmentStore()
	server := &Server{certManager: cm, enrollmentStore: store}

	issued, err := server.IssueAgentIdentity(t.Context(), IssueRequest{
		TenantID:              "tenant-1",
		AgentID:               "agent-1",
		AllowedCommandClasses: []string{"stackkit"},
		EnrolledBy:            "pairing-redemption",
	})
	if err != nil {
		t.Fatalf("IssueAgentIdentity: %v", err)
	}
	if issued.AgentID != "agent-1" || issued.TenantID != "tenant-1" || len(issued.KeyPEM) == 0 {
		t.Fatalf("issued identity = %#v", issued)
	}
	enrollment, err := store.Get(t.Context(), "tenant-1", "agent-1")
	if err != nil {
		t.Fatalf("Get enrollment: %v", err)
	}
	if len(enrollment.AllowedCommandClasses) != 1 || enrollment.AllowedCommandClasses[0] != "stackkit" {
		t.Fatalf("allowed command classes = %#v", enrollment.AllowedCommandClasses)
	}
}

func TestServerIssueAgentIdentityFailsClosedWithoutAuthority(t *testing.T) {
	for _, server := range []*Server{nil, {}} {
		if _, err := server.IssueAgentIdentity(t.Context(), IssueRequest{TenantID: "tenant-1", AgentID: "agent-1"}); err == nil {
			t.Fatal("IssueAgentIdentity succeeded without a configured authority")
		}
	}
}
