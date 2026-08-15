// Package grpcserver — agent enrollment registry.
//
// AgentEnrollmentStore is the SaaS-only Agent → (Tenant, Cert, Policy)
// mapping that backs the Phase-7.1 mTLS-identity binding. Records live in
// the Techstack runtime database (kombify-runtime-db in SaaS; any self-host
// Postgres), RLS-scoped on tenant_id; see the postgres_store
// implementation for the persistent layer. The MemoryStore is used by tests
// and by single-tenant standalone mode.
//
// Wire-up: connection.go Register() consults the store after extracting the
// peer's TLS client cert, and command_dispatch.go enforces the per-agent
// AllowedCommandClasses before queueing any command.
package grpcserver

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrEnrollmentNotFound is returned when no enrollment row matches the
// (tenant, agent) pair.
var ErrEnrollmentNotFound = errors.New("agent enrollment not found")

// AgentEnrollment is the per-agent enrollment record.
//
// AgentID matches the cert Common Name (set by pkg/auth.CertManager when
// the cert is issued). TenantID is the kombify-Cloud tenant the agent
// belongs to and is what RLS policies on `techstack_agent_enrollments`
// match against. CertSerial pins the specific cert that was registered;
// re-issuing a cert under the same CN requires re-enrolling so a stolen
// older cert cannot impersonate the agent.
type AgentEnrollment struct {
	AgentID               string
	TenantID              string
	CertSerial            string
	CertFingerprint       string
	AllowedCommandClasses []string
	EnrolledAt            time.Time
	RevokedAt             *time.Time
	EnrolledBy            string
}

// IsAllowed reports whether cmdClass appears in AllowedCommandClasses.
// An empty AllowedCommandClasses slice means "no commands permitted",
// not "all commands permitted" — the caller must explicitly enroll an agent
// for each command class it should accept.
func (e *AgentEnrollment) IsAllowed(cmdClass string) bool {
	if e == nil {
		return false
	}
	for _, c := range e.AllowedCommandClasses {
		if c == cmdClass {
			return true
		}
	}
	return false
}

// AgentEnrollmentStore reads and writes agent enrollment rows. Get backs the
// gRPC Register path; Upsert backs certificate issuance (the mint-and-enroll
// flow pins the issued cert's serial here). Revocation stays a concrete helper
// on each implementation until the operator API formalizes it.
type AgentEnrollmentStore interface {
	// Get returns the enrollment for the (tenant, agent) pair. Returns
	// ErrEnrollmentNotFound if no row exists. Implementations must apply
	// tenant-isolation (RLS or in-memory filtering) so cross-tenant lookups
	// fail closed.
	Get(ctx context.Context, tenantID, agentID string) (*AgentEnrollment, error)

	// Upsert inserts or refreshes the enrollment for (tenant, agent). Used by
	// the issuance flow to pin the freshly minted cert's serial/fingerprint.
	// TenantID and AgentID must both be non-empty.
	Upsert(ctx context.Context, e AgentEnrollment) error
}

// MemoryAgentEnrollmentStore is the in-memory store used by tests and by
// standalone (no-Postgres) mode. Safe for concurrent use.
type MemoryAgentEnrollmentStore struct {
	mu      sync.RWMutex
	records map[string]*AgentEnrollment // key = tenantID + "|" + agentID
}

// NewMemoryAgentEnrollmentStore returns an empty in-memory store.
func NewMemoryAgentEnrollmentStore() *MemoryAgentEnrollmentStore {
	return &MemoryAgentEnrollmentStore{
		records: make(map[string]*AgentEnrollment),
	}
}

// Set inserts or replaces an enrollment record. Helper for tests / dev mode.
// AgentID and TenantID must both be non-empty.
func (s *MemoryAgentEnrollmentStore) Set(e AgentEnrollment) {
	if e.AgentID == "" || e.TenantID == "" {
		return
	}
	if e.EnrolledAt.IsZero() {
		e.EnrolledAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := e
	s.records[memKey(e.TenantID, e.AgentID)] = &copy
}

// Upsert implements AgentEnrollmentStore. Validates the (tenant, agent) pair
// and delegates to Set.
func (s *MemoryAgentEnrollmentStore) Upsert(_ context.Context, e AgentEnrollment) error {
	if e.AgentID == "" || e.TenantID == "" {
		return fmt.Errorf("agent_enrollment: tenant_id and agent_id are required")
	}
	s.Set(e)
	return nil
}

// Revoke marks the matching enrollment as revoked. Returns
// ErrEnrollmentNotFound if no record exists.
func (s *MemoryAgentEnrollmentStore) Revoke(tenantID, agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[memKey(tenantID, agentID)]
	if !ok {
		return ErrEnrollmentNotFound
	}
	now := time.Now().UTC()
	rec.RevokedAt = &now
	return nil
}

// Get implements AgentEnrollmentStore.
func (s *MemoryAgentEnrollmentStore) Get(_ context.Context, tenantID, agentID string) (*AgentEnrollment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.records[memKey(tenantID, agentID)]
	if !ok {
		return nil, ErrEnrollmentNotFound
	}
	out := *rec
	if rec.AllowedCommandClasses != nil {
		out.AllowedCommandClasses = append([]string(nil), rec.AllowedCommandClasses...)
	}
	return &out, nil
}

func memKey(tenantID, agentID string) string {
	return tenantID + "|" + agentID
}
