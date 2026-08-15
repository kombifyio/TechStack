package stackrouting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const routingTenantGUC = "app.tenant_id"

type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) Get(ctx context.Context, tenantID, stackID string) (*DesiredState, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("%w: database not configured", ErrUnavailable)
	}
	tenantID = strings.TrimSpace(tenantID)
	stackID = strings.TrimSpace(stackID)
	if tenantID == "" || stackID == "" {
		return nil, ErrInvalid
	}
	var state *DesiredState
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var err error
		state, err = scanDesiredState(tx.QueryRowContext(ctx, routingSelect+` WHERE tenant_id = $1 AND stack_id = $2`, tenantID, stackID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	if err != nil {
		return nil, normalizePersistenceError("get routing desired state", err)
	}
	return state, nil
}

func (s *PostgresStore) Put(ctx context.Context, req PutRequest) (*PutResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("%w: database not configured", ErrUnavailable)
	}
	var out *PutResult
	err := s.withTenant(ctx, req.TenantID, func(tx *sql.Tx) error {
		var putErr error
		out, putErr = putRoutingDesiredState(ctx, tx, req)
		return putErr
	})
	if err != nil {
		return nil, normalizePersistenceError("put routing desired state", err)
	}
	return out, nil
}

func putRoutingDesiredState(ctx context.Context, tx *sql.Tx, req PutRequest) (*PutResult, error) {
	// Serialize both first-write and update races for this exact stack. This
	// keeps the idempotency receipt and routing revision in one transaction.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, routingKey(req.TenantID, req.StackID)); err != nil {
		return nil, err
	}
	replay, found, err := loadRoutingIdempotencyReceipt(ctx, tx, req)
	if err != nil || found {
		return replay, err
	}
	current, err := loadCurrentRoutingState(ctx, tx, req.TenantID, req.StackID)
	if err != nil {
		return nil, err
	}
	next, err := persistNextRoutingState(ctx, tx, req, current)
	if err != nil {
		return nil, err
	}
	if err = insertRoutingIdempotencyReceipt(ctx, tx, req, next); err != nil {
		return nil, err
	}
	return &PutResult{State: cloneDesiredState(&next)}, nil
}

func loadRoutingIdempotencyReceipt(ctx context.Context, tx *sql.Tx, req PutRequest) (*PutResult, bool, error) {
	var requestHash string
	var responseJSON []byte
	queryErr := tx.QueryRowContext(ctx, `
		SELECT request_hash, response_json::text
		FROM stack_routing_idempotency
		WHERE tenant_id = $1 AND owner_subject_id = $2 AND idempotency_key = $3
	`, req.TenantID, req.OwnerSubjectID, req.IdempotencyKey).Scan(&requestHash, &responseJSON)
	if errors.Is(queryErr, sql.ErrNoRows) {
		return nil, false, nil
	}
	if queryErr != nil {
		return nil, false, queryErr
	}
	if requestHash != req.RequestHash {
		return nil, true, ErrIdempotencyConflict
	}
	var replay DesiredState
	if decodeErr := json.Unmarshal(responseJSON, &replay); decodeErr != nil {
		return nil, true, fmt.Errorf("decode routing idempotency receipt: %w", decodeErr)
	}
	replay.TenantID = req.TenantID
	replay.OwnerSubjectID = req.OwnerSubjectID
	return &PutResult{State: &replay, Replay: true}, true, nil
}

func loadCurrentRoutingState(ctx context.Context, tx *sql.Tx, tenantID, stackID string) (*DesiredState, error) {
	current, err := scanDesiredState(tx.QueryRowContext(ctx, routingSelect+`
		WHERE tenant_id = $1 AND stack_id = $2
		FOR UPDATE
	`, tenantID, stackID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return current, err
}

func persistNextRoutingState(ctx context.Context, tx *sql.Tx, req PutRequest, current *DesiredState) (DesiredState, error) {
	currentRevision := int64(0)
	if current != nil {
		currentRevision = current.Revision
	}
	if req.ExpectedRevision != nil && *req.ExpectedRevision != currentRevision {
		return DesiredState{}, ErrRevisionConflict
	}
	next := req.DesiredState
	if current != nil && current.RolloutStatus == RolloutPending && !sameDesiredRouting(*current, next) {
		return DesiredState{}, ErrRolloutInProgress
	}
	if current != nil && sameDesiredRouting(*current, next) {
		return *current, nil
	}
	next.Revision = currentRevision + 1
	return insertRoutingDesiredState(ctx, tx, next)
}

func insertRoutingDesiredState(ctx context.Context, tx *sql.Tx, next DesiredState) (DesiredState, error) {
	provenanceJSON, err := json.Marshal(next.Provenance)
	if err != nil {
		return DesiredState{}, err
	}
	return scanDesiredStateValue(tx.QueryRowContext(ctx, `
		INSERT INTO stack_routing_desired (
			tenant_id, stack_id, owner_subject_id, server_id, lease_id,
			revision, mode, domain, provenance_json, rollout_status,
			rollout_job_id, reason_code
		) VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8, $9::jsonb, $10, NULLIF($11, ''), NULLIF($12, ''))
		ON CONFLICT (tenant_id, stack_id) DO UPDATE SET
			owner_subject_id = EXCLUDED.owner_subject_id,
			server_id = EXCLUDED.server_id,
			lease_id = EXCLUDED.lease_id,
			revision = EXCLUDED.revision,
			mode = EXCLUDED.mode,
			domain = EXCLUDED.domain,
			provenance_json = EXCLUDED.provenance_json,
			rollout_status = EXCLUDED.rollout_status,
			rollout_job_id = EXCLUDED.rollout_job_id,
			reason_code = EXCLUDED.reason_code,
			updated_at = now()
		RETURNING tenant_id, stack_id, owner_subject_id, server_id, lease_id,
			revision, mode, domain, provenance_json::text, rollout_status,
			COALESCE(rollout_job_id, ''), COALESCE(reason_code, ''), created_at, updated_at
	`, next.TenantID, next.StackID, next.OwnerSubjectID, next.ServerID, next.LeaseID,
		next.Revision, next.Mode, next.Domain, provenanceJSON, next.RolloutStatus,
		next.RolloutJobID, next.ReasonCode))
}

func insertRoutingIdempotencyReceipt(ctx context.Context, tx *sql.Tx, req PutRequest, state DesiredState) error {
	responseJSON, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO stack_routing_idempotency (
			tenant_id, stack_id, owner_subject_id, idempotency_key, request_hash, response_json
		) VALUES ($1, $2, $3, $4, $5, $6::jsonb)
	`, req.TenantID, req.StackID, req.OwnerSubjectID, req.IdempotencyKey, req.RequestHash, responseJSON)
	return err
}

func (s *PostgresStore) MarkRolloutDispatched(ctx context.Context, tenantID, stackID string, expectedRevision int64, jobID string) (*DesiredState, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("%w: database not configured", ErrUnavailable)
	}
	tenantID = strings.TrimSpace(tenantID)
	stackID = strings.TrimSpace(stackID)
	jobID = strings.TrimSpace(jobID)
	if tenantID == "" || stackID == "" || expectedRevision <= 0 || jobID == "" {
		return nil, ErrInvalid
	}
	var state *DesiredState
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var mutationErr error
		state, mutationErr = markRolloutDispatchedTx(ctx, tx, tenantID, stackID, expectedRevision, jobID)
		return mutationErr
	})
	if err != nil {
		return nil, normalizePersistenceError("mark routing rollout dispatched", err)
	}
	return state, nil
}

func markRolloutDispatchedTx(ctx context.Context, tx *sql.Tx, tenantID, stackID string, expectedRevision int64, jobID string) (*DesiredState, error) {
	state, err := scanDesiredState(tx.QueryRowContext(ctx, `
		UPDATE stack_routing_desired
		SET rollout_status = $4, rollout_job_id = $5, reason_code = NULL, updated_at = now()
		WHERE tenant_id = $1 AND stack_id = $2 AND revision = $3
		  AND rollout_status IN ('not_requested', 'failed')
		RETURNING tenant_id, stack_id, owner_subject_id, server_id, lease_id,
			revision, mode, domain, provenance_json::text, rollout_status,
			COALESCE(rollout_job_id, ''), COALESCE(reason_code, ''), created_at, updated_at
	`, tenantID, stackID, expectedRevision, RolloutPending, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		state, err = loadDispatchedRolloutReplay(ctx, tx, tenantID, stackID, expectedRevision, jobID)
	}
	if err != nil {
		return nil, err
	}
	if err = updateRoutingIdempotencyReceipts(ctx, tx, tenantID, stackID, expectedRevision, state); err != nil {
		return nil, err
	}
	return state, nil
}

func loadDispatchedRolloutReplay(ctx context.Context, tx *sql.Tx, tenantID, stackID string, expectedRevision int64, jobID string) (*DesiredState, error) {
	state, err := loadExistingRoutingStateForUpdate(ctx, tx, tenantID, stackID)
	if err != nil {
		return nil, err
	}
	if state.Revision != expectedRevision || state.RolloutJobID != jobID || !isAcceptedRolloutStatus(state.RolloutStatus) {
		return nil, ErrRevisionConflict
	}
	return state, nil
}

func isAcceptedRolloutStatus(status string) bool {
	switch status {
	case RolloutPending, RolloutCompleted, RolloutFailed:
		return true
	default:
		return false
	}
}

func (s *PostgresStore) MarkRolloutFinished(ctx context.Context, tenantID, stackID string, expectedRevision int64, jobID, terminalStatus, reasonCode string) (*DesiredState, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("%w: database not configured", ErrUnavailable)
	}
	tenantID = strings.TrimSpace(tenantID)
	stackID = strings.TrimSpace(stackID)
	jobID, terminalStatus, reasonCode, err := normalizeFinishedRollout(jobID, terminalStatus, reasonCode)
	if err != nil || tenantID == "" || stackID == "" || expectedRevision <= 0 {
		return nil, ErrInvalid
	}
	var state *DesiredState
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var mutationErr error
		state, mutationErr = markRolloutFinishedTx(ctx, tx, tenantID, stackID, expectedRevision, jobID, terminalStatus, reasonCode)
		return mutationErr
	})
	if err != nil {
		return nil, normalizePersistenceError("finish routing rollout", err)
	}
	return state, nil
}

func markRolloutFinishedTx(ctx context.Context, tx *sql.Tx, tenantID, stackID string, expectedRevision int64, jobID, terminalStatus, reasonCode string) (*DesiredState, error) {
	state, err := scanDesiredState(tx.QueryRowContext(ctx, `
		UPDATE stack_routing_desired
		SET rollout_status = $6, rollout_job_id = $4, reason_code = NULLIF($7, ''), updated_at = now()
		WHERE tenant_id = $1 AND stack_id = $2 AND revision = $3
		  AND ((rollout_job_id = $4 AND rollout_status = $5)
		    OR (rollout_job_id IS NULL AND rollout_status = 'not_requested'))
		RETURNING tenant_id, stack_id, owner_subject_id, server_id, lease_id,
			revision, mode, domain, provenance_json::text, rollout_status,
			COALESCE(rollout_job_id, ''), COALESCE(reason_code, ''), created_at, updated_at
	`, tenantID, stackID, expectedRevision, jobID, RolloutPending, terminalStatus, reasonCode))
	if errors.Is(err, sql.ErrNoRows) {
		state, err = loadFinishedRolloutReplay(ctx, tx, tenantID, stackID, expectedRevision, jobID, terminalStatus)
	}
	if err != nil {
		return nil, err
	}
	if err = updateRoutingIdempotencyReceipts(ctx, tx, tenantID, stackID, expectedRevision, state); err != nil {
		return nil, err
	}
	return state, nil
}

func loadFinishedRolloutReplay(ctx context.Context, tx *sql.Tx, tenantID, stackID string, expectedRevision int64, jobID, terminalStatus string) (*DesiredState, error) {
	state, err := loadExistingRoutingStateForUpdate(ctx, tx, tenantID, stackID)
	if err != nil {
		return nil, err
	}
	if state.Revision != expectedRevision || state.RolloutJobID != jobID || state.RolloutStatus != terminalStatus {
		return nil, ErrRevisionConflict
	}
	return state, nil
}

func loadExistingRoutingStateForUpdate(ctx context.Context, tx *sql.Tx, tenantID, stackID string) (*DesiredState, error) {
	state, err := scanDesiredState(tx.QueryRowContext(ctx, routingSelect+` WHERE tenant_id = $1 AND stack_id = $2 FOR UPDATE`, tenantID, stackID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return state, err
}

func updateRoutingIdempotencyReceipts(ctx context.Context, tx *sql.Tx, tenantID, stackID string, expectedRevision int64, state *DesiredState) error {
	responseJSON, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE stack_routing_idempotency
		SET response_json = $4::jsonb
		WHERE tenant_id = $1 AND stack_id = $2
		  AND (response_json->>'revision')::bigint = $3
	`, tenantID, stackID, expectedRevision, responseJSON)
	return err
}

const routingSelect = `
	SELECT tenant_id, stack_id, owner_subject_id, server_id, lease_id,
		revision, mode, domain, provenance_json::text, rollout_status,
		COALESCE(rollout_job_id, ''), COALESCE(reason_code, ''), created_at, updated_at
	FROM stack_routing_desired`

type routingRowScanner interface {
	Scan(dest ...any) error
}

func scanDesiredState(row routingRowScanner) (*DesiredState, error) {
	state, err := scanDesiredStateValue(row)
	if err != nil {
		return nil, err
	}
	return &state, nil
}

func scanDesiredStateValue(row routingRowScanner) (DesiredState, error) {
	var state DesiredState
	var leaseID sql.NullString
	var provenanceJSON []byte
	err := row.Scan(
		&state.TenantID, &state.StackID, &state.OwnerSubjectID, &state.ServerID, &leaseID,
		&state.Revision, &state.Mode, &state.Domain, &provenanceJSON, &state.RolloutStatus,
		&state.RolloutJobID, &state.ReasonCode, &state.CreatedAt, &state.UpdatedAt,
	)
	if err != nil {
		return DesiredState{}, err
	}
	state.LeaseID = leaseID.String
	if err := json.Unmarshal(provenanceJSON, &state.Provenance); err != nil {
		return DesiredState{}, err
	}
	return state, nil
}

func (s *PostgresStore) withTenant(ctx context.Context, tenantID string, fn func(*sql.Tx) error) error {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, `SELECT set_config($1, $2, true)`, routingTenantGUC, tenantID); err != nil {
		return fmt.Errorf("set routing tenant context: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func normalizePersistenceError(operation string, err error) error {
	for _, contractErr := range []error{ErrNotFound, ErrInvalid, ErrRevisionConflict, ErrIdempotencyConflict, ErrRolloutInProgress} {
		if errors.Is(err, contractErr) {
			return err
		}
	}
	return fmt.Errorf("%w: %s: %v", ErrUnavailable, operation, err)
}

var _ Store = (*PostgresStore)(nil)
