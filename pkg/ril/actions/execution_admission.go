package actions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/runtimelease"
	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
	"github.com/google/uuid"
)

const executionAdmissionVersion = "techstack.ril-execution-admission/v1"

// ExecutionAdmission is Techstack's internal immutable binding between an
// approved action and the current Runtime Inventory and RuntimeLease heads.
// It is never serialized into the provider-free RIL request or public evidence.
type ExecutionAdmission struct {
	InventoryRevision    int64
	ServerRevision       int64
	ServerGeneration     int64
	LeaseID              string
	LeaseRevision        uint64
	ResourceGenerationID string
	RequestDigest        string
	Digest               string
}

type executionAdmissionPayload struct {
	Version              string `json:"version"`
	TenantID             string `json:"tenant_id"`
	OwnerSubjectID       string `json:"owner_subject_id"`
	ServerID             string `json:"server_id"`
	InventoryRevision    int64  `json:"inventory_revision"`
	ServerRevision       int64  `json:"server_revision"`
	ServerGeneration     int64  `json:"server_generation"`
	LeaseID              string `json:"lease_id"`
	LeaseRevision        uint64 `json:"lease_revision"`
	ResourceGenerationID string `json:"resource_generation_id"`
	RequestDigest        string `json:"request_digest"`
}

func admitExecutionTx(ctx context.Context, tx *sql.Tx, card *GovernedCard, request rilaction.Request, now time.Time) (ExecutionAdmission, rilaction.Request, error) {
	var (
		inventoryRevision int64
		serverRevision    int64
		serverGeneration  int64
		workerID          string
		lifecycleState    string
		serverDesired     string
		connectionState   string
		leaseID           string
		leaseRevision     int64
		generationID      string
		desiredState      string
		validFrom         time.Time
		validUntil        time.Time
		cancelledAt       sql.NullTime
	)
	err := tx.QueryRowContext(ctx, `
		SELECT server.inventory_revision, server.revision, server.generation,
		       COALESCE(server.worker_id, ''), server.lifecycle_state,
		       server.desired_state, server.connection_state,
		       lease.id, lease.lease_revision,
		       lease.resource_generation_id::text, lease.desired_state,
		       lease.valid_from, lease.valid_until, lease.cancelled_at
		FROM servers AS server
		JOIN techstack_vm_leases AS lease
		  ON lease.tenant_id = server.tenant_id
		 AND lease.id = server.lease_id
		 AND lease.server_id = server.id
		WHERE server.tenant_id = $1
		  AND server.owner_subject_id = $2
		  AND server.id = $3
		  AND server.stack_id = $4
		  AND lease.owner_subject_id = $2
		FOR UPDATE OF server, lease
	`, request.TenantID, card.OwnerSubjectID, card.ServerID, request.StackID).Scan(
		&inventoryRevision, &serverRevision, &serverGeneration, &workerID,
		&lifecycleState, &serverDesired, &connectionState,
		&leaseID, &leaseRevision, &generationID, &desiredState,
		&validFrom, &validUntil, &cancelledAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ExecutionAdmission{}, rilaction.Request{}, fmt.Errorf("%w: current inventory and runtime lease binding not found", ErrExecutionAdmission)
	}
	if err != nil {
		return ExecutionAdmission{}, rilaction.Request{}, fmt.Errorf("load execution admission: %w", err)
	}
	if inventoryRevision < 1 || serverRevision < 1 || serverGeneration < 1 || leaseRevision < 1 {
		return ExecutionAdmission{}, rilaction.Request{}, fmt.Errorf("%w: current inventory, server, or lease revision is missing", ErrExecutionAdmission)
	}
	if lifecycleState != "active" || serverDesired != "running" || connectionState != "connected" {
		return ExecutionAdmission{}, rilaction.Request{}, fmt.Errorf("%w: runtime server is not active, desired-running, and connected", ErrExecutionAdmission)
	}
	if strings.TrimSpace(workerID) == "" || workerID != request.Target.RuntimeInstanceRef {
		return ExecutionAdmission{}, rilaction.Request{}, fmt.Errorf("%w: execution target is not the current runtime worker", ErrExecutionAdmission)
	}
	if desiredState != string(runtimelease.DesiredStateRunning) {
		return ExecutionAdmission{}, rilaction.Request{}, fmt.Errorf("%w: runtime lease is not running", ErrExecutionAdmission)
	}
	parsedGeneration, err := uuid.Parse(strings.TrimSpace(generationID))
	if err != nil || parsedGeneration.String() != generationID {
		return ExecutionAdmission{}, rilaction.Request{}, fmt.Errorf("%w: runtime lease generation is not canonical", ErrExecutionAdmission)
	}
	lease := runtimelease.Lease{
		ID: runtimelease.LeaseID(leaseID), Revision: uint64(leaseRevision),
		TenantID: request.TenantID, OwnerID: card.OwnerSubjectID,
		ServerID:             runtimelease.RuntimeServerID(card.ServerID),
		ResourceGenerationID: runtimelease.ResourceGenerationID(generationID),
		DesiredState:         runtimelease.DesiredState(desiredState),
		ValidFrom:            validFrom, ValidUntil: validUntil,
	}
	if cancelledAt.Valid {
		lease.CancelledAt = &cancelledAt.Time
	}
	if err := lease.Validate(now); err != nil {
		return ExecutionAdmission{}, rilaction.Request{}, fmt.Errorf("%w: %v", ErrExecutionAdmission, err)
	}
	requestValidUntil, err := time.Parse(time.RFC3339Nano, request.ValidUntil)
	if err != nil {
		return ExecutionAdmission{}, rilaction.Request{}, fmt.Errorf("%w: action request validity is invalid", ErrExecutionAdmission)
	}
	if lease.ValidUntil.Before(requestValidUntil) {
		request.ValidUntil = lease.ValidUntil.UTC().Format(time.RFC3339Nano)
	}
	if err := rilaction.ValidateRequestAt(request, now); err != nil {
		return ExecutionAdmission{}, rilaction.Request{}, err
	}
	requestDigest, err := rilaction.ComputeRequestDigest(request)
	if err != nil {
		return ExecutionAdmission{}, rilaction.Request{}, err
	}
	payload := executionAdmissionPayload{
		Version: executionAdmissionVersion, TenantID: request.TenantID,
		OwnerSubjectID: card.OwnerSubjectID, ServerID: card.ServerID,
		InventoryRevision: inventoryRevision, ServerRevision: serverRevision,
		ServerGeneration: serverGeneration, LeaseID: leaseID,
		LeaseRevision: uint64(leaseRevision), ResourceGenerationID: generationID,
		RequestDigest: requestDigest,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ExecutionAdmission{}, rilaction.Request{}, fmt.Errorf("encode execution admission: %w", err)
	}
	return ExecutionAdmission{
		InventoryRevision: inventoryRevision, ServerRevision: serverRevision,
		ServerGeneration: serverGeneration, LeaseID: leaseID,
		LeaseRevision: uint64(leaseRevision), ResourceGenerationID: generationID,
		RequestDigest: requestDigest, Digest: digestText(string(data)),
	}, request, nil
}
