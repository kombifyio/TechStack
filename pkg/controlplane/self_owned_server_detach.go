package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/serverregistry"
)

var (
	ErrServerDetachConfirmation = errors.New("controlplane: exact server detach confirmation required")
	ErrServerDetachUnsupported  = errors.New("controlplane: server is not customer-operated BYO infrastructure")
	ErrServerAgentCustody       = errors.New("controlplane: exact server agent enrollment is unavailable")
)

const selfOwnedServerDetachSource = "owner-byo-detach"

// SelfOwnedServerDetachRequest binds an Owner-approved detach to one exact
// tenant/server/agent aggregate. ConfirmServerID is deliberately redundant:
// mutation surfaces must echo the concrete server selected by the Owner.
type SelfOwnedServerDetachRequest struct {
	TenantID        string
	OwnerSubjectID  string
	ServerID        string
	ConfirmServerID string
}

// SelfOwnedServerDetachReceipt is the secret-free terminal readback. The
// canonical aggregate transition timeline remains the durable audit record.
type SelfOwnedServerDetachReceipt struct {
	ServerID   string    `json:"server_id"`
	AgentID    string    `json:"agent_id"`
	Revision   int64     `json:"revision"`
	Generation int64     `json:"generation"`
	DetachedAt time.Time `json:"detached_at"`
	Replay     bool      `json:"replay"`
}

type SelfOwnedServerDetacher interface {
	DetachSelfOwnedServer(context.Context, SelfOwnedServerDetachRequest) (*SelfOwnedServerDetachReceipt, error)
}

// DetachSelfOwnedServer atomically revokes the exact Guard enrollment and
// advances a customer-operated BYO RuntimeServer to its terminal tombstone.
// It performs no provider I/O and never deletes the canonical aggregate.
func (s *PostgresStore) DetachSelfOwnedServer(ctx context.Context, request SelfOwnedServerDetachRequest) (*SelfOwnedServerDetachReceipt, error) {
	request.TenantID = strings.TrimSpace(request.TenantID)
	request.OwnerSubjectID = strings.TrimSpace(request.OwnerSubjectID)
	request.ServerID = strings.TrimSpace(request.ServerID)
	request.ConfirmServerID = strings.TrimSpace(request.ConfirmServerID)
	if request.TenantID == "" || request.OwnerSubjectID == "" || request.ServerID == "" {
		return nil, fmt.Errorf("controlplane: server detach tenant, owner, and server are required")
	}
	if request.ConfirmServerID != request.ServerID {
		return nil, ErrServerDetachConfirmation
	}
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}

	var receipt *SelfOwnedServerDetachReceipt
	err := s.withTenant(ctx, request.TenantID, func(tx *sql.Tx) error {
		var now time.Time
		if err := tx.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
			return fmt.Errorf("controlplane: read server detach database time: %w", err)
		}
		now = now.UTC()
		current, err := scanServerRuntime(tx.QueryRowContext(ctx, `
			SELECT `+serverRuntimeColumns+` FROM servers
			WHERE tenant_id = $1 AND id = $2
			FOR UPDATE
		`, request.TenantID, request.ServerID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if strings.TrimSpace(current.OwnerSubjectID) != request.OwnerSubjectID {
			return ErrNotFound
		}
		if !selfOwnedServerDetachAllowed(*current) {
			return ErrServerDetachUnsupported
		}
		agentID := strings.TrimSpace(current.WorkerID)
		if agentID == "" {
			return ErrServerAgentCustody
		}

		var revokedAt time.Time
		err = tx.QueryRowContext(ctx, `
			UPDATE techstack_agent_enrollments
			SET revoked_at = COALESCE(revoked_at, $3)
			WHERE tenant_id = $1 AND agent_id = $2
			RETURNING revoked_at
		`, request.TenantID, agentID, now).Scan(&revokedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrServerAgentCustody
		}
		if err != nil {
			return fmt.Errorf("controlplane: revoke server agent enrollment: %w", err)
		}
		workerResult, err := tx.ExecContext(ctx, `
			UPDATE workers
			SET status = 'revoked', updated_at = $4
			WHERE tenant_id = $1 AND id = $2 AND owner_subject_id = $3
		`, request.TenantID, agentID, request.OwnerSubjectID, now)
		if err != nil {
			return fmt.Errorf("controlplane: revoke server worker projection: %w", err)
		}
		if affected, _ := workerResult.RowsAffected(); affected != 1 {
			return ErrServerAgentCustody
		}

		replay := current.LifecycleState == string(serverregistry.LifecycleDecommissioned) &&
			current.DesiredState == string(serverregistry.DesiredAbsent) && current.DecommissionedAt != nil
		terminal := current
		if !replay {
			if current.LifecycleState != string(serverregistry.LifecycleDecommissioning) {
				started, applyErr := applyServerEventTx(ctx, tx, ServerEvent{
					TenantID: request.TenantID, ServerID: request.ServerID,
					ExpectedRevision: current.Revision, Generation: current.Generation,
					Authority: ServerEventAuthorityControlPlane, Source: selfOwnedServerDetachSource,
					SourceID: request.OwnerSubjectID, ObservedAt: now,
					Runtime: ServerRuntime{
						LifecycleState:      string(serverregistry.LifecycleDecommissioning),
						DesiredState:        string(serverregistry.DesiredAbsent),
						LifecycleReasonCode: "owner_detach_requested",
						DesiredReasonCode:   "owner_detach_requested",
					},
					Evidence: selfOwnedServerDetachEvidence(agentID, false),
				}, now, s.serverEventProjector)
				if applyErr != nil {
					return applyErr
				}
				terminal = started.Server
			}
			completed, applyErr := applyServerEventTx(ctx, tx, ServerEvent{
				TenantID: request.TenantID, ServerID: request.ServerID,
				ExpectedRevision: terminal.Revision, Generation: terminal.Generation,
				Authority: ServerEventAuthorityControlPlane, Source: selfOwnedServerDetachSource,
				SourceID: request.OwnerSubjectID, ObservedAt: now,
				Runtime: ServerRuntime{
					LifecycleState:      string(serverregistry.LifecycleDecommissioned),
					DesiredState:        string(serverregistry.DesiredAbsent),
					LifecycleReasonCode: "owner_detach_completed",
					DesiredReasonCode:   "owner_detach_requested",
					DecommissionedAt:    &now,
				},
				Evidence: selfOwnedServerDetachEvidence(agentID, true),
			}, now, s.serverEventProjector)
			if applyErr != nil {
				return applyErr
			}
			terminal = completed.Server
		}
		detachedAt := revokedAt.UTC()
		if terminal.DecommissionedAt != nil {
			detachedAt = terminal.DecommissionedAt.UTC()
		}
		receipt = &SelfOwnedServerDetachReceipt{
			ServerID: terminal.ID, AgentID: agentID, Revision: terminal.Revision,
			Generation: terminal.Generation, DetachedAt: detachedAt, Replay: replay,
		}
		return nil
	})
	return receipt, err
}

// SelfOwnedServerDetachAllowed reports whether this aggregate may be detached
// by its owner without provider mutation. The route must not advertise detach
// unless this predicate is true.
func SelfOwnedServerDetachAllowed(server ServerRuntime) bool {
	return selfOwnedServerDetachAllowed(server)
}

func selfOwnedServerDetachAllowed(server ServerRuntime) bool {
	target := serverregistry.NormalizeRuntimeTarget(server.RuntimeTarget)
	if strings.TrimSpace(server.LeaseID) != "" || target.OperationsOwner != serverregistry.OperationsCustomer ||
		serverregistry.ValidateRuntimeTarget(target, server.LeaseID) != nil {
		return false
	}
	return target.EnvironmentClass == serverregistry.EnvironmentLocal && target.Offering == serverregistry.OfferingSelfOwnedDevice ||
		target.EnvironmentClass == serverregistry.EnvironmentCloud && target.Offering == serverregistry.OfferingExternalVPS
}

func selfOwnedServerDetachEvidence(agentID string, terminal bool) map[string]any {
	return map[string]any{
		"action": "self_owned_server_detach", "agent_id": agentID,
		"provider_mutation": false, "terminal": terminal,
	}
}
