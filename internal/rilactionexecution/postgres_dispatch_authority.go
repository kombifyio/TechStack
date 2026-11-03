package rilactionexecution

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/guardreach"
	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
	"github.com/kombifyio/techstack/pkg/ril/actions"
	"github.com/kombifyio/techstack/pkg/ril/nodeproof"
)

// PostgresDispatchAuthority holds the admitted server and lease heads locked
// while the typed command is dispatched, so the worker cannot be selected from
// a stale inventory projection or from the provider-free stack target.
type PostgresDispatchAuthority struct{ DB *sql.DB }

func (a PostgresDispatchAuthority) OwnerFor(ctx context.Context, request rilaction.Request, admission string) (string, error) {
	if a.DB == nil {
		return "", fmt.Errorf("RIL authority unavailable")
	}
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', $1, true)", request.TenantID); err != nil {
		return "", err
	}
	var owner string
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT owner_subject_id, execution_request_json FROM ril_action_cards WHERE tenant_id=$1 AND id=$2 AND execution_id=$3 AND execution_admission_digest=$4 AND status='executing'`, request.TenantID, request.ActionCardID, request.ExecutionID, admission).Scan(&owner, &raw)
	if err != nil {
		return "", err
	}
	var stored rilaction.Request
	if json.Unmarshal(raw, &stored) != nil {
		return "", fmt.Errorf("invalid admitted request")
	}
	want, err := rilaction.ComputeRequestDigest(request)
	if err != nil {
		return "", err
	}
	got, err := rilaction.ComputeRequestDigest(stored)
	if err != nil || got != want || owner == "" {
		return "", fmt.Errorf("admitted owner mismatch")
	}
	return owner, nil
}

func (a PostgresDispatchAuthority) WithWorker(ctx context.Context, request rilaction.Request, admission string, proof *agentpb.RILExecutionProof, dispatch func(context.Context, string) error) error {
	if a.DB == nil || dispatch == nil {
		return fmt.Errorf("RIL dispatch authority unavailable")
	}
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', $1, true)", request.TenantID); err != nil {
		return err
	}
	var worker, owner, server string
	var saved, proposalRaw []byte
	var cardRevision, serverRevision, leaseRevision int64
	var validUntil time.Time
	err = tx.QueryRowContext(ctx, `
 SELECT server.worker_id, card.execution_request_json, lease.valid_until, card.owner_subject_id, server.id, card.decision_proposal_json, card.decision_card_revision, server.revision, lease.lease_revision
 FROM ril_action_cards card
 JOIN servers server ON server.tenant_id=card.tenant_id AND server.id=card.server_id
   AND server.owner_subject_id=card.owner_subject_id
 JOIN techstack_vm_leases lease ON lease.tenant_id=server.tenant_id AND lease.id=server.lease_id
   AND lease.server_id=server.id AND lease.owner_subject_id=card.owner_subject_id
 WHERE card.tenant_id=$1 AND card.id=$2 AND card.execution_id=$3
   AND card.execution_admission_digest=$4 AND card.status='executing'
   AND server.stack_id=$5 AND server.inventory_revision=card.admission_inventory_revision
   AND server.revision=card.admission_server_revision AND server.generation=card.admission_server_generation
   AND server.lifecycle_state='active' AND server.desired_state='running' AND server.connection_state='connected'
   AND NULLIF(BTRIM(server.worker_id), '') IS NOT NULL
   AND lease.id=card.admission_lease_id AND lease.lease_revision=card.admission_lease_revision
   AND lease.resource_generation_id=card.admission_resource_generation_id
   AND lease.desired_state='running' AND lease.cancelled_at IS NULL
   AND lease.valid_from <= clock_timestamp() AND lease.valid_until > clock_timestamp()
 FOR UPDATE OF card, server, lease`, request.TenantID, request.ActionCardID, request.ExecutionID, admission, request.StackID).Scan(&worker, &saved, &validUntil, &owner, &server, &proposalRaw, &cardRevision, &serverRevision, &leaseRevision)
	if err != nil {
		return fmt.Errorf("resolve admitted RIL worker: %w", err)
	}
	var persisted rilaction.Request
	if err := json.Unmarshal(saved, &persisted); err != nil {
		return fmt.Errorf("decode admitted RIL request: %w", err)
	}
	expected, err := rilaction.ComputeRequestDigest(request)
	if err != nil {
		return err
	}
	actual, err := rilaction.ComputeRequestDigest(persisted)
	if err != nil || actual != expected {
		return fmt.Errorf("RIL request differs from admitted authority")
	}
	var proposal struct {
		EffectDigest       string `json:"effectDigest"`
		LifecycleRevision  int64  `json:"lifecycleRevision"`
		LifecycleExpiresAt int64  `json:"lifecycleExpiresAt"`
		ServerRevision     int64  `json:"serverRevision"`
		Effect             struct {
			CardRevision int64 `json:"cardRevision"`
		} `json:"effect"`
	}
	if json.Unmarshal(proposalRaw, &proposal) != nil {
		return fmt.Errorf("RIL proposal unavailable")
	}
	facts, err := nodeproof.InspectTransport(proof)
	if err != nil {
		return err
	}
	if facts.ExecutionID != request.ExecutionID || facts.Fence != admission || facts.TenantID != request.TenantID || facts.OwnerSubject != owner || facts.ServerID != server || facts.StackID != request.StackID || facts.EffectDigest != proposal.EffectDigest || facts.ReceiptDigest != request.Approval.ReceiptHash || proposal.Effect.CardRevision != cardRevision || proposal.LifecycleRevision != leaseRevision || facts.LifecycleRevision != leaseRevision || proposal.ServerRevision != serverRevision || facts.RuntimeLeaseExpiresAt != proposal.LifecycleExpiresAt || facts.RuntimeLeaseExpiresAt > validUntil.Unix() || facts.Exp > facts.RuntimeLeaseExpiresAt || facts.Exp <= time.Now().Unix() {
		return fmt.Errorf("RIL proof differs from locked proposal and lifecycle heads")
	}
	deadline, err := time.Parse(time.RFC3339Nano, request.ValidUntil)
	if err != nil {
		return err
	}
	if validUntil.Before(deadline) {
		deadline = validUntil
	}
	if !time.Now().Before(deadline) || strings.TrimSpace(worker) == "" {
		return fmt.Errorf("RIL dispatch authority expired")
	}
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := dispatch(bounded, worker); err != nil {
		return err
	}
	return tx.Commit()
}

// IssuedLease opens only the authority sealed in the same transaction that
// admitted this exact execution. It is never returned through workflow state.
func (a PostgresDispatchAuthority) IssuedLease(ctx context.Context, request rilaction.Request, admission string) (guardreach.RILAuthorityResult, error) {
	var empty guardreach.RILAuthorityResult
	if a.DB == nil {
		return empty, fmt.Errorf("RIL authority unavailable")
	}
	tx, err := a.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', $1, true)", request.TenantID); err != nil {
		return empty, err
	}
	var sealed []byte
	var saved []byte
	err = tx.QueryRowContext(ctx, `SELECT issued_lease_ciphertext, execution_request_json FROM ril_action_cards WHERE tenant_id=$1 AND id=$2 AND execution_id=$3 AND execution_admission_digest=$4 AND status='executing' AND issued_lease_expires_at>clock_timestamp()`, request.TenantID, request.ActionCardID, request.ExecutionID, admission).Scan(&sealed, &saved)
	if err != nil {
		return empty, err
	}
	var persisted rilaction.Request
	if json.Unmarshal(saved, &persisted) != nil {
		return empty, fmt.Errorf("invalid admitted request")
	}
	expected, err := rilaction.ComputeRequestDigest(request)
	if err != nil {
		return empty, err
	}
	actual, err := rilaction.ComputeRequestDigest(persisted)
	if err != nil || actual != expected {
		return empty, fmt.Errorf("admitted request differs")
	}
	issued, err := actions.OpenRILIssuedLease(request.TenantID, request.ActionCardID, request.ExecutionID, request.Grant.BindingHash, sealed)
	if err != nil {
		return empty, err
	}
	sum := sha256.Sum256([]byte(issued.Lease))
	if "sha256:"+hex.EncodeToString(sum[:]) != request.Grant.BindingHash {
		return empty, fmt.Errorf("RIL issued lease mismatch")
	}
	return issued, nil
}
