package grpcserver

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/logger"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// makeClientCert returns a minimal client certificate matching the supplied
// CommonName, Organization, and serial. The cert is self-signed because the
// Phase-7.1 binding in Register() never re-validates the chain — it only
// inspects fields. Real TLS chain verification is the gRPC stack's job.
func makeClientCert(t *testing.T, cn, org string, serial *big.Int) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate cert key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   cn,
			Organization: []string{org},
		},
		NotBefore: time.Now().Add(-time.Hour),
		NotAfter:  time.Now().Add(time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert
}

// ctxWithPeerCert builds a gRPC peer context carrying the given client cert.
func ctxWithPeerCert(cert *x509.Certificate) context.Context {
	tlsInfo := credentials.TLSInfo{
		State: tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{cert},
		},
	}
	addr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:5263")
	p := &peer.Peer{Addr: addr, AuthInfo: tlsInfo}
	return peer.NewContext(context.Background(), p)
}

func newTestServerWithEnrollment(t *testing.T, store AgentEnrollmentStore) *Server {
	t.Helper()
	srv, err := New(Config{ListenAddr: ":0"}, logger.Get())
	if err != nil {
		t.Fatalf("New server: %v", err)
	}
	srv.SetEnrollmentStore(store)
	return srv
}

func TestRegister_Phase71_HappyPath(t *testing.T) {
	store := NewMemoryAgentEnrollmentStore()
	cert := makeClientCert(t, "agent-1", "tenant-A", big.NewInt(42))
	store.Set(AgentEnrollment{
		AgentID:               "agent-1",
		TenantID:              "tenant-A",
		CertSerial:            cert.SerialNumber.String(),
		CertFingerprint:       "fingerprint-x",
		AllowedCommandClasses: []string{"health_check", "get_logs"},
		EnrolledBy:            "operator@example.com",
	})

	srv := newTestServerWithEnrollment(t, store)
	ctx := ctxWithPeerCert(cert)

	resp, err := srv.Register(ctx, &agentpb.RegisterRequest{
		AgentId:  "agent-1",
		Hostname: "host-1",
	})
	if err != nil {
		t.Fatalf("Register err: %v", err)
	}
	if !resp.Accepted {
		t.Fatalf("Register did not accept happy-path agent")
	}

	got, ok := srv.GetAgent("agent-1")
	if !ok {
		t.Fatal("agent not in registry after Register")
	}
	if got.Tenant != "tenant-A" {
		t.Errorf("tenant = %q, want tenant-A", got.Tenant)
	}
	if got.CertSerial != cert.SerialNumber.String() {
		t.Errorf("cert serial = %q, want %q", got.CertSerial, cert.SerialNumber.String())
	}
	if len(got.AllowedCommandClasses) != 2 {
		t.Errorf("allowed classes = %v, want [health_check, get_logs]", got.AllowedCommandClasses)
	}
}

func TestRegister_Phase71_RejectsCertCNMismatch(t *testing.T) {
	store := NewMemoryAgentEnrollmentStore()
	cert := makeClientCert(t, "agent-A", "tenant-A", big.NewInt(1))

	srv := newTestServerWithEnrollment(t, store)
	ctx := ctxWithPeerCert(cert)

	_, err := srv.Register(ctx, &agentpb.RegisterRequest{AgentId: "agent-B"})
	assertGRPCCode(t, err, codes.Unauthenticated, "CN")
}

func TestRegister_Phase71_RejectsMissingPeerCert(t *testing.T) {
	store := NewMemoryAgentEnrollmentStore()
	srv := newTestServerWithEnrollment(t, store)

	_, err := srv.Register(context.Background(), &agentpb.RegisterRequest{AgentId: "agent-1"})
	assertGRPCCode(t, err, codes.Unauthenticated, "peer certificate")
}

func TestRegister_Phase71_RejectsMissingEnrollment(t *testing.T) {
	store := NewMemoryAgentEnrollmentStore()
	cert := makeClientCert(t, "agent-1", "tenant-A", big.NewInt(7))

	srv := newTestServerWithEnrollment(t, store)
	ctx := ctxWithPeerCert(cert)

	_, err := srv.Register(ctx, &agentpb.RegisterRequest{AgentId: "agent-1"})
	assertGRPCCode(t, err, codes.PermissionDenied, "not enrolled")
}

func TestRegister_Phase71_RejectsTenantMismatch(t *testing.T) {
	store := NewMemoryAgentEnrollmentStore()
	cert := makeClientCert(t, "agent-1", "tenant-A", big.NewInt(8))
	// Enrollment exists, but for a DIFFERENT tenant. The cert's
	// Organization=tenant-A drives the lookup, which won't find the
	// tenant-B row.
	store.Set(AgentEnrollment{
		AgentID:    "agent-1",
		TenantID:   "tenant-B",
		CertSerial: cert.SerialNumber.String(),
	})

	srv := newTestServerWithEnrollment(t, store)
	ctx := ctxWithPeerCert(cert)

	_, err := srv.Register(ctx, &agentpb.RegisterRequest{AgentId: "agent-1"})
	assertGRPCCode(t, err, codes.PermissionDenied, "not enrolled")
}

func TestRegister_Phase71_RejectsRevokedEnrollment(t *testing.T) {
	store := NewMemoryAgentEnrollmentStore()
	cert := makeClientCert(t, "agent-1", "tenant-A", big.NewInt(9))
	store.Set(AgentEnrollment{
		AgentID:    "agent-1",
		TenantID:   "tenant-A",
		CertSerial: cert.SerialNumber.String(),
	})
	if err := store.Revoke("tenant-A", "agent-1"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	srv := newTestServerWithEnrollment(t, store)
	ctx := ctxWithPeerCert(cert)

	_, err := srv.Register(ctx, &agentpb.RegisterRequest{AgentId: "agent-1"})
	assertGRPCCode(t, err, codes.PermissionDenied, "revoked")
}

func TestRegister_Phase71_RejectsCertSerialMismatch(t *testing.T) {
	store := NewMemoryAgentEnrollmentStore()
	cert := makeClientCert(t, "agent-1", "tenant-A", big.NewInt(10))
	// Enrolled with a DIFFERENT serial number — simulates a stolen older
	// cert being presented after the agent was re-enrolled.
	store.Set(AgentEnrollment{
		AgentID:    "agent-1",
		TenantID:   "tenant-A",
		CertSerial: "999",
	})

	srv := newTestServerWithEnrollment(t, store)
	ctx := ctxWithPeerCert(cert)

	_, err := srv.Register(ctx, &agentpb.RegisterRequest{AgentId: "agent-1"})
	assertGRPCCode(t, err, codes.Unauthenticated, "serial")
}

func TestSendCommand_Phase71_RejectsForbiddenCommandClass(t *testing.T) {
	store := NewMemoryAgentEnrollmentStore()
	cert := makeClientCert(t, "agent-1", "tenant-A", big.NewInt(11))
	store.Set(AgentEnrollment{
		AgentID:               "agent-1",
		TenantID:              "tenant-A",
		CertSerial:            cert.SerialNumber.String(),
		AllowedCommandClasses: []string{"health_check"},
	})

	srv := newTestServerWithEnrollment(t, store)
	ctx := ctxWithPeerCert(cert)
	if _, err := srv.Register(ctx, &agentpb.RegisterRequest{AgentId: "agent-1"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	t.Run("allowed command passes", func(t *testing.T) {
		err := srv.SendCommand(&AgentCommand{
			ID:      "cmd-1",
			AgentID: "agent-1",
			Type:    "health_check",
		})
		if err != nil {
			t.Errorf("allowed command rejected: %v", err)
		}
	})

	t.Run("forbidden command rejected", func(t *testing.T) {
		err := srv.SendCommand(&AgentCommand{
			ID:      "cmd-2",
			AgentID: "agent-1",
			Type:    "get_logs",
		})
		if err == nil {
			t.Fatal("expected forbidden command to be rejected")
		}
		if !strings.Contains(err.Error(), "allowlist") {
			t.Errorf("err = %v, want containing 'allowlist'", err)
		}
	})
}

func TestSendCommand_Phase71_RejectsEmptyCommandAllowlist(t *testing.T) {
	store := NewMemoryAgentEnrollmentStore()
	cert := makeClientCert(t, "agent-1", "tenant-A", big.NewInt(12))
	store.Set(AgentEnrollment{
		AgentID:               "agent-1",
		TenantID:              "tenant-A",
		CertSerial:            cert.SerialNumber.String(),
		AllowedCommandClasses: nil,
	})

	srv := newTestServerWithEnrollment(t, store)
	ctx := ctxWithPeerCert(cert)
	if _, err := srv.Register(ctx, &agentpb.RegisterRequest{AgentId: "agent-1"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	err := srv.SendCommand(&AgentCommand{
		ID:      "cmd-1",
		AgentID: "agent-1",
		Type:    "health_check",
	})
	if err == nil {
		t.Fatal("expected command to be rejected when enrollment has no allowed command classes")
	}
	if !strings.Contains(err.Error(), "allowlist") {
		t.Errorf("err = %v, want containing 'allowlist'", err)
	}
}

func TestCommandStream_Phase71_RejectsUnregisteredAgent(t *testing.T) {
	store := NewMemoryAgentEnrollmentStore()
	cert := makeClientCert(t, "agent-1", "tenant-A", big.NewInt(13))
	store.Set(AgentEnrollment{
		AgentID:               "agent-1",
		TenantID:              "tenant-A",
		CertSerial:            cert.SerialNumber.String(),
		AllowedCommandClasses: []string{"health_check"},
	})

	srv := newTestServerWithEnrollment(t, store)
	stream := newFakeCommandStream(ctxWithPeerCert(cert), &agentpb.AgentMessage{AgentId: "agent-1"})

	err := srv.CommandStream(stream)
	assertGRPCCode(t, err, codes.PermissionDenied, "not registered")
}

func TestCommandStream_Phase71_RejectsPeerCertAgentMismatch(t *testing.T) {
	store := NewMemoryAgentEnrollmentStore()
	cert := makeClientCert(t, "agent-1", "tenant-A", big.NewInt(14))
	store.Set(AgentEnrollment{
		AgentID:               "agent-1",
		TenantID:              "tenant-A",
		CertSerial:            cert.SerialNumber.String(),
		AllowedCommandClasses: []string{"health_check"},
	})

	srv := newTestServerWithEnrollment(t, store)
	if _, err := srv.Register(ctxWithPeerCert(cert), &agentpb.RegisterRequest{AgentId: "agent-1"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	otherCert := makeClientCert(t, "agent-2", "tenant-A", big.NewInt(15))
	stream := newFakeCommandStream(ctxWithPeerCert(otherCert), &agentpb.AgentMessage{AgentId: "agent-1"})

	err := srv.CommandStream(stream)
	assertGRPCCode(t, err, codes.Unauthenticated, "CN")
}

func TestCommandStream_Phase71_AcceptsRegisteredMatchingPeer(t *testing.T) {
	store := NewMemoryAgentEnrollmentStore()
	cert := makeClientCert(t, "agent-1", "tenant-A", big.NewInt(16))
	store.Set(AgentEnrollment{
		AgentID:               "agent-1",
		TenantID:              "tenant-A",
		CertSerial:            cert.SerialNumber.String(),
		AllowedCommandClasses: []string{"health_check"},
	})

	srv := newTestServerWithEnrollment(t, store)
	if _, err := srv.Register(ctxWithPeerCert(cert), &agentpb.RegisterRequest{AgentId: "agent-1"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	stream := newFakeCommandStream(ctxWithPeerCert(cert), &agentpb.AgentMessage{AgentId: "agent-1"})

	if err := srv.CommandStream(stream); err != nil {
		t.Fatalf("CommandStream: %v", err)
	}
}

func TestHeartbeat_Phase71_RejectsUnregisteredAgent(t *testing.T) {
	store := NewMemoryAgentEnrollmentStore()
	cert := makeClientCert(t, "agent-1", "tenant-A", big.NewInt(17))
	store.Set(AgentEnrollment{
		AgentID:               "agent-1",
		TenantID:              "tenant-A",
		CertSerial:            cert.SerialNumber.String(),
		AllowedCommandClasses: []string{"health_check"},
	})

	srv := newTestServerWithEnrollment(t, store)

	_, err := srv.Heartbeat(ctxWithPeerCert(cert), &agentpb.HeartbeatRequest{AgentId: "agent-1"})
	assertGRPCCode(t, err, codes.PermissionDenied, "not registered")
}

func TestHeartbeat_Phase71_RejectsPeerCertAgentMismatch(t *testing.T) {
	store := NewMemoryAgentEnrollmentStore()
	cert := makeClientCert(t, "agent-1", "tenant-A", big.NewInt(18))
	store.Set(AgentEnrollment{
		AgentID:               "agent-1",
		TenantID:              "tenant-A",
		CertSerial:            cert.SerialNumber.String(),
		AllowedCommandClasses: []string{"health_check"},
	})

	srv := newTestServerWithEnrollment(t, store)
	if _, err := srv.Register(ctxWithPeerCert(cert), &agentpb.RegisterRequest{AgentId: "agent-1"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	otherCert := makeClientCert(t, "agent-2", "tenant-A", big.NewInt(19))
	_, err := srv.Heartbeat(ctxWithPeerCert(otherCert), &agentpb.HeartbeatRequest{AgentId: "agent-1"})
	assertGRPCCode(t, err, codes.Unauthenticated, "CN")
}

func TestPushMetrics_Phase71_RejectsPeerCertAgentMismatch(t *testing.T) {
	store := NewMemoryAgentEnrollmentStore()
	cert := makeClientCert(t, "agent-1", "tenant-A", big.NewInt(20))
	store.Set(AgentEnrollment{
		AgentID:               "agent-1",
		TenantID:              "tenant-A",
		CertSerial:            cert.SerialNumber.String(),
		AllowedCommandClasses: []string{"health_check"},
	})

	srv := newTestServerWithEnrollment(t, store)
	srv.SetMonitorTSDB(newTestTSDB(t))
	if _, err := srv.Register(ctxWithPeerCert(cert), &agentpb.RegisterRequest{AgentId: "agent-1"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	otherCert := makeClientCert(t, "agent-2", "tenant-A", big.NewInt(21))
	_, err := srv.PushMetrics(ctxWithPeerCert(otherCert), &agentpb.MetricsBatch{
		AgentId: "agent-1",
		Samples: []*agentpb.MetricSample{
			{Name: "cpu_usage", Value: 1, TimestampUnix: time.Now().UnixMilli()},
		},
	})
	assertGRPCCode(t, err, codes.Unauthenticated, "CN")
}

func TestReportStatus_Phase71_RejectsPeerCertAgentMismatch(t *testing.T) {
	store := NewMemoryAgentEnrollmentStore()
	cert := makeClientCert(t, "agent-1", "tenant-A", big.NewInt(22))
	store.Set(AgentEnrollment{
		AgentID:               "agent-1",
		TenantID:              "tenant-A",
		CertSerial:            cert.SerialNumber.String(),
		AllowedCommandClasses: []string{"health_check"},
	})

	srv := newTestServerWithEnrollment(t, store)
	if _, err := srv.Register(ctxWithPeerCert(cert), &agentpb.RegisterRequest{AgentId: "agent-1"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	otherCert := makeClientCert(t, "agent-2", "tenant-A", big.NewInt(23))
	_, err := srv.ReportStatus(ctxWithPeerCert(otherCert), &agentpb.StatusReport{
		AgentId: "agent-1",
		Node:    &agentpb.NodeStatus{State: "healthy"},
	})
	assertGRPCCode(t, err, codes.Unauthenticated, "CN")
}

func TestRegister_Phase71_LegacyModeNoEnrollmentStore(t *testing.T) {
	// When neither certManager nor enrollmentStore is wired, Register
	// preserves the pre-7.1 behaviour: any agent_id is accepted, no cert
	// extraction is attempted.
	srv, err := New(Config{ListenAddr: ":0"}, logger.Get())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	resp, err := srv.Register(context.Background(), &agentpb.RegisterRequest{
		AgentId: "anything-goes",
	})
	if err != nil {
		t.Fatalf("legacy Register err: %v", err)
	}
	if !resp.Accepted {
		t.Fatal("legacy Register did not accept agent")
	}
}

type fakeCommandStream struct {
	ctx      context.Context
	messages []*agentpb.AgentMessage
	sent     []*agentpb.CoreMessage
	next     int
}

func newFakeCommandStream(ctx context.Context, messages ...*agentpb.AgentMessage) *fakeCommandStream {
	return &fakeCommandStream{ctx: ctx, messages: messages}
}

func (s *fakeCommandStream) Recv() (*agentpb.AgentMessage, error) {
	if s.next >= len(s.messages) {
		return nil, io.EOF
	}
	msg := s.messages[s.next]
	s.next++
	return msg, nil
}

func (s *fakeCommandStream) Send(msg *agentpb.CoreMessage) error {
	s.sent = append(s.sent, msg)
	return nil
}

func (s *fakeCommandStream) SetHeader(metadata.MD) error {
	return nil
}

func (s *fakeCommandStream) SendHeader(metadata.MD) error {
	return nil
}

func (s *fakeCommandStream) SetTrailer(metadata.MD) {}

func (s *fakeCommandStream) Context() context.Context {
	if s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

func (s *fakeCommandStream) SendMsg(any) error {
	return nil
}

func (s *fakeCommandStream) RecvMsg(any) error {
	return nil
}

func assertGRPCCode(t *testing.T, err error, want codes.Code, msgFragment string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %s, got nil", want)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got: %v", err)
	}
	if st.Code() != want {
		t.Fatalf("code = %s, want %s; message = %q", st.Code(), want, st.Message())
	}
	if msgFragment != "" && !strings.Contains(st.Message(), msgFragment) {
		t.Errorf("message = %q, want containing %q", st.Message(), msgFragment)
	}
}
