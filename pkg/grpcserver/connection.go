// Package grpcserver implements the gRPC server for agent communication.
// This file handles agent connection lifecycle: registration, disconnection, and connection management.
package grpcserver

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

const agentStatusConnected, agentStatusDisconnected = "connected", "disconnected"

// Register implements agentpb.AgentServiceServer.Register
//
// Phase 7.1 — mTLS identity binding. When an enrollment store is wired
// (production / SaaS path), Register validates that the peer's TLS client
// cert matches the claimed AgentId, that an active enrollment row exists,
// and that the cert serial pinned in the enrollment matches the cert that
// terminated the TLS handshake. ConnectedAgent.Tenant +
// AllowedCommandClasses are pinned from the enrollment row. When no
// enrollment store is wired (legacy / standalone mode), the cert checks
// run if a CertManager is wired but the registry lookup is skipped.
func (s *Server) Register(ctx context.Context, req *agentpb.RegisterRequest) (*agentpb.RegisterResponse, error) {
	peerInfo, _ := peer.FromContext(ctx)
	remoteAddr := "unknown"
	if peerInfo != nil {
		remoteAddr = peerInfo.Addr.String()
	}

	s.log.Info("agent_register_request",
		"agent_id", req.AgentId,
		"hostname", req.Hostname,
		"remote", remoteAddr,
	)

	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}

	tenant, certSerial, allowedClasses, err := s.bindAgentIdentity(ctx, req.AgentId, peerInfo)
	if err != nil {
		// bindAgentIdentity already returns codes-tagged errors.
		return nil, err
	}

	agent := &ConnectedAgent{
		ID:                    req.AgentId,
		Hostname:              req.Hostname,
		OS:                    req.Os,
		Arch:                  req.Arch,
		Version:               req.Version,
		Capabilities:          req.Capabilities,
		ConnectedAt:           time.Now(),
		LastSeen:              time.Now(),
		Status:                agentStatusConnected,
		Tenant:                tenant,
		CertSerial:            certSerial,
		AllowedCommandClasses: allowedClasses,
	}

	if err := s.RegisterAgent(agent); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to register agent: %v", err)
	}

	return &agentpb.RegisterResponse{
		Accepted: true,
		Config: &agentpb.AgentConfig{
			HeartbeatIntervalSeconds: int32(s.heartbeatTimeout.Seconds() / 3), // Heartbeat 3x before timeout
		},
	}, nil
}

// bindAgentIdentity runs the Phase-7.1 cert ↔ enrollment validation chain.
// Returns the tenant, cert serial, and allowed command classes that should
// be pinned onto the ConnectedAgent. Returns codes-tagged errors on
// rejection.
//
// Validation chain (skipped sections noted explicitly):
//   - peer cert extraction: REQUIRED whenever certManager OR enrollmentStore is wired
//   - cert.Subject.CommonName == req.AgentId
//   - (optional) certManager.VerifyAgentCert (signature, expiry, revocation)
//   - enrollment lookup: REQUIRED whenever enrollmentStore is wired
//   - enrollment not revoked
//   - enrollment.CertSerial == cert.SerialNumber
//
// When no Phase-7.1 plumbing is wired (both fields nil), the function
// returns the configured defaultTenant and an empty allowlist — preserving
// the pre-7.1 behaviour for legacy callers and tests.
func (s *Server) bindAgentIdentity(ctx context.Context, agentID string, peerInfo *peer.Peer) (string, string, []string, error) {
	// Legacy path: nothing wired.
	if s.certManager == nil && s.enrollmentStore == nil {
		return s.defaultTenant, "", nil, nil
	}

	cert, err := extractPeerCert(peerInfo)
	if err != nil {
		return "", "", nil, status.Errorf(codes.Unauthenticated, "peer certificate required: %v", err)
	}

	if cert.Subject.CommonName != agentID {
		return "", "", nil, status.Errorf(codes.Unauthenticated,
			"client cert CN %q does not match agent_id %q",
			cert.Subject.CommonName, agentID,
		)
	}

	if s.certManager != nil {
		if s.certManager.IsRevoked(cert.SerialNumber.String()) {
			return "", "", nil, status.Error(codes.Unauthenticated, "client certificate has been revoked")
		}
	}

	tenant := pickTenantFromCert(cert, s.defaultTenant)

	// If only certManager is wired (no enrollment store), accept the cert
	// without per-agent policy. AllowedCommandClasses stays empty so the
	// command_dispatch enforcer rejects everything until an enrollment is
	// added — this is the safer default than "allow everything".
	if s.enrollmentStore == nil {
		return tenant, cert.SerialNumber.String(), nil, nil
	}

	enrollment, err := s.enrollmentStore.Get(ctx, tenant, agentID)
	if err != nil {
		if errors.Is(err, ErrEnrollmentNotFound) {
			return "", "", nil, status.Errorf(codes.PermissionDenied,
				"agent %q is not enrolled for tenant %q", agentID, tenant,
			)
		}
		return "", "", nil, status.Errorf(codes.Internal, "enrollment lookup failed: %v", err)
	}

	if enrollment.RevokedAt != nil {
		return "", "", nil, status.Errorf(codes.PermissionDenied,
			"agent %q enrollment is revoked", agentID,
		)
	}

	if enrollment.CertSerial != cert.SerialNumber.String() {
		return "", "", nil, status.Errorf(codes.Unauthenticated,
			"client cert serial does not match enrollment record",
		)
	}

	return enrollment.TenantID, cert.SerialNumber.String(), append([]string(nil), enrollment.AllowedCommandClasses...), nil
}

// authorizeCommandStream re-checks the same cert and enrollment chain for the
// command delivery stream. Register() binds the initial connection; this guard
// prevents a later stream from claiming another registered agent_id.
func (s *Server) authorizeCommandStream(ctx context.Context, agentID string) error {
	_, err := s.authorizeRegisteredAgentRPC(ctx, agentID)
	return err
}

func (s *Server) requiresAgentIdentityBinding() bool {
	return s.certManager != nil || s.enrollmentStore != nil
}

// authorizeRegisteredAgentRPC verifies that a request for agentID is coming
// from the already-registered agent and, when Phase-7.1 wiring is active,
// from the same mTLS certificate/enrollment identity. Legacy standalone mode
// still only requires that the agent previously registered.
func (s *Server) authorizeRegisteredAgentRPC(ctx context.Context, agentID string) (*ConnectedAgent, error) {
	s.agentsMu.RLock()
	agent, ok := s.agents[agentID]
	if ok {
		agent = &ConnectedAgent{
			ID:                    agent.ID,
			Status:                agent.Status,
			Tenant:                agent.Tenant,
			CertSerial:            agent.CertSerial,
			AllowedCommandClasses: append([]string(nil), agent.AllowedCommandClasses...),
		}
	}
	s.agentsMu.RUnlock()

	if !ok {
		return nil, status.Errorf(codes.PermissionDenied, "agent %q is not registered", agentID)
	}
	if agent.Status == agentStatusDisconnected {
		return nil, status.Errorf(codes.PermissionDenied, "agent %q is disconnected", agentID)
	}

	if !s.requiresAgentIdentityBinding() {
		return agent, nil
	}

	peerInfo, _ := peer.FromContext(ctx)
	tenant, certSerial, allowedClasses, err := s.bindAgentIdentity(ctx, agentID, peerInfo)
	if err != nil {
		return nil, err
	}

	if tenant != "" && agent.Tenant != "" && tenant != agent.Tenant {
		return nil, status.Errorf(codes.PermissionDenied, "agent %q tenant mismatch", agentID)
	}
	if certSerial != "" && agent.CertSerial != "" && certSerial != agent.CertSerial {
		return nil, status.Errorf(codes.Unauthenticated, "client cert serial does not match registered agent")
	}

	if s.enrollmentStore != nil {
		s.agentsMu.Lock()
		if current, ok := s.agents[agentID]; ok {
			current.Tenant = tenant
			current.CertSerial = certSerial
			current.AllowedCommandClasses = append([]string(nil), allowedClasses...)
		}
		s.agentsMu.Unlock()
	}

	agent.Tenant = tenant
	agent.CertSerial = certSerial
	agent.AllowedCommandClasses = append([]string(nil), allowedClasses...)
	return agent, nil
}

// extractPeerCert pulls the first peer certificate out of a gRPC Peer's
// TLS auth info, returning a typed error if the chain is missing.
func extractPeerCert(peerInfo *peer.Peer) (*x509.Certificate, error) {
	if peerInfo == nil {
		return nil, fmt.Errorf("no peer information in context")
	}
	tlsInfo, ok := peerInfo.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return nil, fmt.Errorf("connection is not TLS")
	}
	if len(tlsInfo.State.PeerCertificates) == 0 {
		return nil, fmt.Errorf("no client certificate presented")
	}
	return tlsInfo.State.PeerCertificates[0], nil
}

// pickTenantFromCert pulls the tenant identifier out of a client cert.
// Convention: the first non-empty Organization OU acts as the tenant.
// Falls back to the server's defaultTenant when the cert carries no
// tenant data.
func pickTenantFromCert(cert *x509.Certificate, fallback string) string {
	if cert == nil {
		return fallback
	}
	for _, org := range cert.Subject.Organization {
		if org != "" {
			return org
		}
	}
	return fallback
}

// ReportStatus implements agentpb.AgentServiceServer.ReportStatus
func (s *Server) ReportStatus(ctx context.Context, req *agentpb.StatusReport) (*agentpb.StatusAck, error) {
	if req.AgentId == "" {
		return nil, status.Error(codes.InvalidArgument, "agent_id is required")
	}
	if s.requiresAgentIdentityBinding() {
		if _, err := s.authorizeRegisteredAgentRPC(ctx, req.AgentId); err != nil {
			return nil, err
		}
	}

	// Determine status from node state
	nodeState := "unknown"
	if req.Node != nil {
		nodeState = req.Node.State
	}

	s.log.Info("status_report_received",
		"agent_id", req.AgentId,
		"node_state", nodeState,
		"services_count", len(req.Services),
	)

	// Update agent status
	s.agentsMu.Lock()
	if agent, ok := s.agents[req.AgentId]; ok {
		agent.LastSeen = time.Now()
		if req.Node != nil {
			agent.Status = req.Node.State
			// Update resources if available
			if req.Node.Resources != nil {
				agent.Resources = &ResourceUsage{
					CPUPercent:       req.Node.Resources.CpuPercent,
					MemoryUsedBytes:  req.Node.Resources.MemoryUsedBytes,
					MemoryTotalBytes: req.Node.Resources.MemoryTotalBytes,
					DiskUsedBytes:    req.Node.Resources.DiskUsedBytes,
					DiskTotalBytes:   req.Node.Resources.DiskTotalBytes,
				}
			}
		}
	}
	s.agentsMu.Unlock()

	return &agentpb.StatusAck{
		Received:            true,
		NextReportInSeconds: 30, // Request next report in 30 seconds
	}, nil
}

// RegisterAgent registers a new agent.
func (s *Server) RegisterAgent(agent *ConnectedAgent) error {
	s.agentsMu.Lock()
	defer s.agentsMu.Unlock()

	agent.ConnectedAt = time.Now()
	agent.LastSeen = time.Now()
	agent.Status = agentStatusConnected

	s.agents[agent.ID] = agent
	s.log.Info("agent_registered", "id", agent.ID, "hostname", agent.Hostname)

	return nil
}

// GetAgents returns all connected agents.
func (s *Server) GetAgents() []*ConnectedAgent {
	s.agentsMu.RLock()
	defer s.agentsMu.RUnlock()

	agents := make([]*ConnectedAgent, 0, len(s.agents))
	for _, agent := range s.agents {
		agents = append(agents, agent)
	}
	return agents
}

// GetAgent returns a specific agent by ID.
func (s *Server) GetAgent(id string) (*ConnectedAgent, bool) {
	s.agentsMu.RLock()
	defer s.agentsMu.RUnlock()

	agent, ok := s.agents[id]
	return agent, ok
}

// RemoveAgent removes an agent from the server.
// Note: The agent's gRPC stream will be terminated when it next tries to communicate.
func (s *Server) RemoveAgent(id string) error {
	s.agentsMu.Lock()
	defer s.agentsMu.Unlock()

	agent, ok := s.agents[id]
	if !ok {
		return fmt.Errorf("agent not found: %s", id)
	}

	// Mark as disconnected and remove from the agents map
	agent.Status = agentStatusDisconnected
	delete(s.agents, id)

	// Cleanup in-memory logs and subscribers
	s.cleanupAgentLogs(id)

	s.log.Info("agent_removed", "id", id, "hostname", agent.Hostname)
	return nil
}
