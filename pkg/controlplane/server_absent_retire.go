package controlplane

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

var (
	// ErrServerAbsenceUnsupported means the server is leased or operated by
	// kombify or a provider. Its absence is proven by provider-control read-back,
	// never by the owner's word.
	ErrServerAbsenceUnsupported = errors.New("controlplane: server absence is proven by its provider, not by the owner")
	// ErrServerNotOffline means the exact server's Guard reported within the
	// stale-heartbeat window, so it is not provably gone.
	ErrServerNotOffline = errors.New("controlplane: server reported recently and is not offline")
)

const absentOwnerServerRetireSource = "owner-confirmed-server-absence"

// AbsentOwnerServerRetireRequest binds the retirement of one owner-operated
// server whose absence the owner confirmed. EvidenceRef names the control-plane
// record that carries that confirmation (the destroy job), so the server's own
// transition timeline points at it.
type AbsentOwnerServerRetireRequest struct {
	TenantID       string
	OwnerSubjectID string
	ServerID       string
	AgentID        string
	EvidenceRef    string
}

// AbsentOwnerServerRetirer revokes the custody of a server that is gone.
type AbsentOwnerServerRetirer interface {
	RetireAbsentOwnerServer(context.Context, AbsentOwnerServerRetireRequest) (*SelfOwnedServerDetachReceipt, error)
}

// CheckOwnerServerAbsentEligible is the single predicate for taking an
// owner's word that a server is gone. It admits only a server nobody else
// vouches for (no lease, a customer-operated target) whose Guard has
// been silent beyond the stale-heartbeat window, or whose custody already
// ended. It reads no provider and no agent. The connection is recomputed from
// the last heartbeat against now, never trusted from the stored state alone.
func CheckOwnerServerAbsentEligible(server ServerRuntime, now time.Time) error {
	target := serverregistry.NormalizeRuntimeTarget(server.RuntimeTarget)
	// Only a customer-operated target is eligible: a machine the owner brought
	// (an unclassified one included) or a rented VPS the owner operates. Any
	// other operations owner or offering is vouched for by someone else.
	customerOperated := (target.OperationsOwner == "" || target.OperationsOwner == serverregistry.OperationsCustomer) &&
		(target.Offering == "" || target.Offering == serverregistry.OfferingSelfOwnedDevice || target.Offering == serverregistry.OfferingExternalVPS)
	if strings.TrimSpace(server.LeaseID) != "" || !customerOperated {
		return ErrServerAbsenceUnsupported
	}
	if serverregistry.HasDecommissionIntent(serverregistry.LifecycleState(strings.TrimSpace(server.LifecycleState))) ||
		serverregistry.ConnectionState(strings.TrimSpace(server.ConnectionState)) == serverregistry.ConnectionRevoked {
		return nil
	}
	if server.LastHeartbeatAt == nil || server.LastHeartbeatAt.IsZero() ||
		now.Sub(server.LastHeartbeatAt.UTC()) <= runtimehealth.StaleHeartbeatWindow {
		return ErrServerNotOffline
	}
	return nil
}

// RetireAbsentOwnerServer atomically revokes the exact Guard custody (worker
// credential, host-key identity, reach, and the gRPC enrollment when one
// exists) of a server the owner confirmed gone and advances it to the terminal
// tombstone. It performs no provider I/O and never deletes the aggregate.
// Replays return the same receipt.
func (s *PostgresStore) RetireAbsentOwnerServer(ctx context.Context, request AbsentOwnerServerRetireRequest) (*SelfOwnedServerDetachReceipt, error) {
	request.TenantID = strings.TrimSpace(request.TenantID)
	request.OwnerSubjectID = strings.TrimSpace(request.OwnerSubjectID)
	request.ServerID = strings.TrimSpace(request.ServerID)
	request.AgentID = strings.TrimSpace(request.AgentID)
	request.EvidenceRef = strings.TrimSpace(request.EvidenceRef)
	if request.TenantID == "" || request.OwnerSubjectID == "" || request.ServerID == "" || request.AgentID == "" || request.EvidenceRef == "" {
		return nil, fmt.Errorf("controlplane: absent server retirement needs tenant, owner, server, agent and evidence")
	}
	return s.detachOwnerServer(ctx, SelfOwnedServerDetachRequest{
		TenantID: request.TenantID, OwnerSubjectID: request.OwnerSubjectID,
		ServerID: request.ServerID, ConfirmServerID: request.ServerID,
	}, request.AgentID, ownerServerDetachPolicy{
		source:          absentOwnerServerRetireSource,
		requestedReason: "owner_confirmed_server_absent", completedReason: "owner_confirmed_server_absent_completed",
		eligible: func(current ServerRuntime, now time.Time) error {
			return CheckOwnerServerAbsentEligible(current, now)
		},
		evidence: func(agentID string, terminal bool) map[string]any {
			return map[string]any{
				"action": "owner_confirmed_server_absence", "agent_id": agentID, "evidence_ref": request.EvidenceRef,
				"provider_mutation": false, "terminal": terminal,
			}
		},
	})
}

var _ AbsentOwnerServerRetirer = (*PostgresStore)(nil)
