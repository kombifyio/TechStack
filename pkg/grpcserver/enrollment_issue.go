package grpcserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/auth"
)

// IssueRequest describes a certificate to mint for an agent and the enrollment
// to pin for it.
type IssueRequest struct {
	TenantID string
	AgentID  string
	// AllowedCommandClasses is the per-agent command policy. Empty means the
	// agent may connect but no command class is authorized (fail-closed) —
	// callers must name every class the agent should accept.
	AllowedCommandClasses []string
	// ValidDays defaults to 365 when <= 0.
	ValidDays int
	// EnrolledBy records the operator/flow that issued the identity (audit).
	EnrolledBy string
}

// IssuedIdentity is the bundle returned to an agent after enrollment. The agent
// writes CertPEM/KeyPEM/CACertPEM to disk and runs `techstack agent` with them.
type IssuedIdentity struct {
	AgentID    string
	TenantID   string
	CertPEM    []byte
	KeyPEM     []byte
	CACertPEM  []byte
	Serial     string
	ExpiresAt  time.Time
	EnrolledAt time.Time
}

// IssueAgentIdentity mints a tenant-scoped client certificate and pins its
// serial in the enrollment store, so a subsequent gRPC Register with that cert
// passes the Phase-7.1 identity binding (CN == agent_id, Organization ==
// tenant, serial == enrollment). This is the reusable primitive behind the
// pairing-token redemption and connect-flow enrollment paths.
//
// The cert is minted first, then the enrollment upserted; if the upsert fails
// the caller gets an error and must not hand the cert to the agent (an
// un-pinned cert is rejected at Register, so a leaked-but-unenrolled cert
// cannot connect — fail-closed).
func IssueAgentIdentity(ctx context.Context, cm *auth.CertManager, store AgentEnrollmentStore, req IssueRequest) (*IssuedIdentity, error) {
	if cm == nil {
		return nil, fmt.Errorf("issue: cert manager is required")
	}
	if store == nil {
		return nil, fmt.Errorf("issue: enrollment store is required")
	}
	if strings.TrimSpace(req.TenantID) == "" || strings.TrimSpace(req.AgentID) == "" {
		return nil, fmt.Errorf("issue: tenant_id and agent_id are required")
	}

	gc, err := cm.GenerateAgentCertForTenant(req.AgentID, req.TenantID, req.ValidDays)
	if err != nil {
		return nil, fmt.Errorf("issue: mint agent cert: %w", err)
	}

	enrolledAt := time.Now().UTC()
	enrollment := AgentEnrollment{
		AgentID:               req.AgentID,
		TenantID:              req.TenantID,
		CertSerial:            gc.Serial,
		CertFingerprint:       gc.Fingerprint,
		AllowedCommandClasses: append([]string(nil), req.AllowedCommandClasses...),
		EnrolledAt:            enrolledAt,
		EnrolledBy:            req.EnrolledBy,
	}
	if err := store.Upsert(ctx, enrollment); err != nil {
		return nil, fmt.Errorf("issue: pin enrollment: %w", err)
	}

	return &IssuedIdentity{
		AgentID:    req.AgentID,
		TenantID:   req.TenantID,
		CertPEM:    gc.CertPEM,
		KeyPEM:     gc.KeyPEM,
		CACertPEM:  cm.CACertPEM(),
		Serial:     gc.Serial,
		ExpiresAt:  gc.ExpiresAt,
		EnrolledAt: enrolledAt,
	}, nil
}
