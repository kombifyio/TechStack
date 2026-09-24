package providercontrol

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
	"github.com/google/uuid"
)

const provisionResolutionTokenContextKey = "app.provider_resolution_token"

// ProvisionResolutionStore is the durable internal seam used by a future
// admin-authorized Operator Reconcile application. It performs no discovery
// network call and exposes no dispatch or retry capability.
type ProvisionResolutionStore interface {
	LoadProvisionResolutionRevision(context.Context, string, string) (uint64, error)
	LoadProvisionDiscovery(context.Context, string, string, string) (ProvisionDiscoveryObservation, bool, error)
	RecordProvisionDiscovery(context.Context, RecordProvisionDiscoveryRequest) (ProvisionDiscoveryObservation, bool, error)
	CommitProvisionResolution(context.Context, ProvisionResolutionRequest) (ProvisionResolutionDecision, OperationRecord, bool, error)
}

// LoadProvisionResolutionRevision returns the durable revision that selects
// the next immutable discovery/decision replay keys.
func (s *PostgresProvisionResolutionStore) LoadProvisionResolutionRevision(
	ctx context.Context,
	tenantID, operationID string,
) (uint64, error) {
	if s == nil || s.ledger == nil || s.verifier == nil {
		return 0, fmt.Errorf("%w: provision resolution store is not configured", ErrInvalidRequest)
	}
	tenantID = strings.TrimSpace(tenantID)
	operationID = strings.TrimSpace(operationID)
	if err := validateResolutionID("tenant_id", tenantID); err != nil {
		return 0, err
	}
	if err := validateResolutionID("operation_id", operationID); err != nil {
		return 0, err
	}
	var revision uint64
	err := s.ledger.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		resolutionContext, loadErr := loadProvisionResolutionContextTx(ctx, tx, tenantID, operationID)
		if loadErr == nil {
			revision = resolutionContext.binding.ResolutionRevision
		}
		return loadErr
	})
	return revision, err
}

// PostgresProvisionResolutionStore persists immutable discovery evidence and
// atomically adopts an exact candidate into the provider-control ledger.
type PostgresProvisionResolutionStore struct {
	ledger   *PostgresLedger
	verifier ProvisionResolutionVerifier
}

// NewPostgresProvisionResolutionStore creates a fail-closed store. A verifier
// is mandatory even for a zero-candidate observation because resource-less
// provider searches cannot be represented by providerexecutor evidence.
func NewPostgresProvisionResolutionStore(
	ledger *PostgresLedger,
	verifier ProvisionResolutionVerifier,
) (*PostgresProvisionResolutionStore, error) {
	if ledger == nil || ledger.db == nil || verifier == nil {
		return nil, fmt.Errorf("%w: ledger and provision resolution verifier are required", ErrInvalidRequest)
	}
	return &PostgresProvisionResolutionStore{ledger: ledger, verifier: verifier}, nil
}

type provisionResolutionContext struct {
	record  OperationRecord
	binding provisionDiscoveryBinding
}

// LoadProvisionDiscovery returns the immutable observation for an exact
// operation/idempotency tuple. Operator tooling uses this only to resume a
// decision after discovery was durably recorded but the subsequent CAS did
// not commit. The caller must reverify the provider state before deciding.
func (s *PostgresProvisionResolutionStore) LoadProvisionDiscovery(
	ctx context.Context,
	tenantID, operationID, idempotencyKey string,
) (ProvisionDiscoveryObservation, bool, error) {
	if s == nil || s.ledger == nil || s.verifier == nil {
		return ProvisionDiscoveryObservation{}, false, fmt.Errorf("%w: provision resolution store is not configured", ErrInvalidRequest)
	}
	tenantID = strings.TrimSpace(tenantID)
	operationID = strings.TrimSpace(operationID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	for name, value := range map[string]string{
		"tenant_id": tenantID, "operation_id": operationID, "idempotency_key": idempotencyKey,
	} {
		if err := validateResolutionID(name, value); err != nil {
			return ProvisionDiscoveryObservation{}, false, err
		}
	}
	var observation ProvisionDiscoveryObservation
	var found bool
	err := s.ledger.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var loadErr error
		observation, found, loadErr = loadProvisionDiscoveryByIdempotencyTx(
			ctx, tx, tenantID, operationID, idempotencyKey,
		)
		return loadErr
	})
	return observation, found, err
}

// RecordProvisionDiscovery appends one certified, read-only discovery
// snapshot. It never mutates an operation, claim, guard, or provider resource.
func (s *PostgresProvisionResolutionStore) RecordProvisionDiscovery(
	ctx context.Context,
	req RecordProvisionDiscoveryRequest,
) (ProvisionDiscoveryObservation, bool, error) {
	if s == nil || s.ledger == nil || s.verifier == nil {
		return ProvisionDiscoveryObservation{}, false, fmt.Errorf("%w: provision resolution store is not configured", ErrInvalidRequest)
	}
	req.TenantID = strings.TrimSpace(req.TenantID)
	req.OperationID = strings.TrimSpace(req.OperationID)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)

	var existing ProvisionDiscoveryObservation
	var found bool
	if err := s.ledger.withTenant(ctx, req.TenantID, func(tx *sql.Tx) error {
		var loadErr error
		existing, found, loadErr = loadProvisionDiscoveryByIdempotencyTx(ctx, tx, req.TenantID, req.OperationID, req.IdempotencyKey)
		return loadErr
	}); err != nil {
		return ProvisionDiscoveryObservation{}, false, err
	}
	if found {
		if !provisionDiscoveryRequestMatches(existing, req) {
			return ProvisionDiscoveryObservation{}, false, ErrProvisionResolutionConflict
		}
		return existing, false, nil
	}

	var resolutionContext provisionResolutionContext
	if err := s.ledger.withTenant(ctx, req.TenantID, func(tx *sql.Tx) error {
		var loadErr error
		resolutionContext, loadErr = loadProvisionResolutionContextTx(ctx, tx, req.TenantID, req.OperationID)
		return loadErr
	}); err != nil {
		return ProvisionDiscoveryObservation{}, false, err
	}
	resolutionContext.binding.ObservationID = deterministicProvisionObservationID(req)
	observation, err := sealProvisionDiscoveryObservation(
		ctx, resolutionContext.record, resolutionContext.binding, req, s.verifier,
	)
	if err != nil {
		return ProvisionDiscoveryObservation{}, false, err
	}
	candidateJSON, err := json.Marshal(observation.Candidates)
	if err != nil {
		return ProvisionDiscoveryObservation{}, false, fmt.Errorf("providercontrol: encode provision candidates: %w", err)
	}

	created := false
	err = s.ledger.withTenant(ctx, req.TenantID, func(tx *sql.Tx) error {
		var recordErr error
		observation, created, recordErr = recordProvisionDiscoveryTx(
			ctx, tx, req, resolutionContext, observation, candidateJSON,
		)
		return recordErr
	})
	if err != nil {
		return ProvisionDiscoveryObservation{}, false, err
	}
	return observation, created, nil
}

func recordProvisionDiscoveryTx(
	ctx context.Context,
	tx *sql.Tx,
	req RecordProvisionDiscoveryRequest,
	resolutionContext provisionResolutionContext,
	observation ProvisionDiscoveryObservation,
	candidateJSON []byte,
) (ProvisionDiscoveryObservation, bool, error) {
	if err := lockProvisionResolutionOperationTx(ctx, tx, req.TenantID, req.OperationID); err != nil {
		return ProvisionDiscoveryObservation{}, false, err
	}
	replay, found, err := loadProvisionDiscoveryByIdempotencyTx(
		ctx, tx, req.TenantID, req.OperationID, req.IdempotencyKey,
	)
	if err != nil {
		return ProvisionDiscoveryObservation{}, false, err
	}
	if found {
		if replay.RequestDigest != observation.RequestDigest {
			return ProvisionDiscoveryObservation{}, false, ErrProvisionResolutionConflict
		}
		return replay, false, nil
	}
	current, err := loadProvisionResolutionContextTx(ctx, tx, req.TenantID, req.OperationID)
	if err != nil {
		return ProvisionDiscoveryObservation{}, false, err
	}
	if !sameProvisionResolutionContext(current, resolutionContext) {
		return ProvisionDiscoveryObservation{}, false, ErrProvisionResolutionConflict
	}
	err = tx.QueryRowContext(ctx, `
			INSERT INTO provider_provision_discovery_observations (
				tenant_id, operation_id, observation_id, lease_id, lease_revision,
				server_id, resource_generation_id, head_sequence, head_receipt_digest,
				observed_resolution_revision, adapter_manifest_hash,
				prepared_request_digest, credential_version_hash, provider_scope_hash,
				correlation_hash, guarded_at, candidate_count, candidate_graphs_json,
				observation_ref, observation_digest, attestation_ref, attestation_digest,
				collected_at, requested_by_subject_id, idempotency_key, request_digest,
				snapshot_digest, recorded_at
			) VALUES (
				$1,$2,$3::uuid,$4,$5,$6,$7::uuid,$8,$9,$10,$11,
				$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,
				clock_timestamp()
			)
			ON CONFLICT (tenant_id, operation_id, idempotency_key) DO NOTHING
			RETURNING recorded_at
		`, observation.TenantID, observation.OperationID, observation.ObservationID,
		observation.LeaseID, int64(observation.LeaseRevision), observation.RuntimeServerID, // #nosec G115 -- the sealed command bounds lease revisions to MaxJSONSafeInteger.
		observation.ResourceGenerationID, int64(observation.HeadSequence), observation.HeadReceiptDigest, // #nosec G115 -- the sealed receipt and resolution guard bound the head sequence.
		int64(observation.ObservedResolutionRevision), observation.AdapterManifestHash, // #nosec G115 -- validateProvisionResolutionGuard reserves room below MaxJSONSafeInteger.
		observation.PreparedBinding.RequestDigest, observation.PreparedBinding.CredentialVersionHash,
		observation.PreparedBinding.ProviderScopeHash, observation.PreparedBinding.CorrelationHash,
		observation.GuardedAt, len(observation.Candidates), candidateJSON, observation.ObservationRef, observation.ObservationDigest,
		observation.AttestationRef, observation.AttestationDigest, observation.CollectedAt,
		observation.RequestedBySubjectID, observation.IdempotencyKey, observation.RequestDigest,
		observation.SnapshotDigest,
	).Scan(&observation.RecordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		replay, found, replayErr := loadProvisionDiscoveryByIdempotencyTx(
			ctx, tx, req.TenantID, req.OperationID, req.IdempotencyKey,
		)
		if replayErr != nil {
			return ProvisionDiscoveryObservation{}, false, replayErr
		}
		if !found || replay.RequestDigest != observation.RequestDigest {
			return ProvisionDiscoveryObservation{}, false, ErrProvisionResolutionConflict
		}
		return replay, false, nil
	}
	if err != nil {
		return ProvisionDiscoveryObservation{}, false, fmt.Errorf("providercontrol: record provision discovery: %w", err)
	}
	return observation, true, nil
}

// CommitProvisionResolution derives the outcome from the immutable discovery
// snapshot. Exact-one adoption, its receipt/resource/evidence custody, and the
// operation-head CAS commit atomically; the other outcomes append only the
// decision and keep the accepted head blocked.
func (s *PostgresProvisionResolutionStore) CommitProvisionResolution(
	ctx context.Context,
	req ProvisionResolutionRequest,
) (ProvisionResolutionDecision, OperationRecord, bool, error) {
	if s == nil || s.ledger == nil || s.verifier == nil {
		return ProvisionResolutionDecision{}, OperationRecord{}, false, fmt.Errorf("%w: provision resolution store is not configured", ErrInvalidRequest)
	}
	sealedRequest, err := sealProvisionResolutionRequest(req)
	if err != nil {
		return ProvisionResolutionDecision{}, OperationRecord{}, false, err
	}

	existing, existingFound, err := s.loadProvisionResolutionReplay(ctx, sealedRequest)
	if err != nil {
		return ProvisionResolutionDecision{}, OperationRecord{}, false, err
	}
	if existingFound {
		return existing.decision, existing.record, false, nil
	}

	resolutionContext, observation, err := s.loadProvisionResolutionInputs(ctx, sealedRequest)
	if err != nil {
		return ProvisionResolutionDecision{}, OperationRecord{}, false, err
	}
	if err := verifyProvisionResolutionOperatorAttestation(observation, sealedRequest); err != nil {
		return ProvisionResolutionDecision{}, OperationRecord{}, false, err
	}
	if err := s.verifier.VerifyProvisionDecision(ctx, resolutionContext.record, observation, sealedRequest); err != nil {
		return ProvisionResolutionDecision{}, OperationRecord{}, false,
			provisionResolutionVerificationError(ctx, err)
	}
	token, err := executionClaimToken()
	if err != nil {
		return ProvisionResolutionDecision{}, OperationRecord{}, false, err
	}

	result, err := s.commitProvisionResolution(ctx, sealedRequest, resolutionContext, token)
	if err != nil {
		return ProvisionResolutionDecision{}, OperationRecord{}, false, err
	}
	return result.decision, result.record, result.created, nil
}

type provisionResolutionCommitResult struct {
	decision ProvisionResolutionDecision
	record   OperationRecord
	created  bool
}

func (s *PostgresProvisionResolutionStore) loadProvisionResolutionReplay(
	ctx context.Context,
	req ProvisionResolutionRequest,
) (provisionResolutionCommitResult, bool, error) {
	var result provisionResolutionCommitResult
	var found bool
	err := s.ledger.withTenant(ctx, req.TenantID, func(tx *sql.Tx) error {
		var loadErr error
		result.decision, found, loadErr = loadProvisionDecisionByIdempotencyTx(
			ctx, tx, req.TenantID, req.OperationID, req.IdempotencyKey,
		)
		if loadErr != nil || !found {
			return loadErr
		}
		result.record, loadErr = loadOperationProjectionTx(
			ctx, tx, req.TenantID, req.OperationID, false, false,
		)
		return loadErr
	})
	if err != nil {
		return provisionResolutionCommitResult{}, false, err
	}
	if found && result.decision.RequestDigest != req.RequestDigest {
		return provisionResolutionCommitResult{}, false, ErrProvisionResolutionConflict
	}
	return result, found, nil
}

func (s *PostgresProvisionResolutionStore) loadProvisionResolutionInputs(
	ctx context.Context,
	req ProvisionResolutionRequest,
) (provisionResolutionContext, ProvisionDiscoveryObservation, error) {
	var resolutionContext provisionResolutionContext
	var observation ProvisionDiscoveryObservation
	err := s.ledger.withTenant(ctx, req.TenantID, func(tx *sql.Tx) error {
		var loadErr error
		resolutionContext, loadErr = loadProvisionResolutionContextTx(
			ctx, tx, req.TenantID, req.OperationID,
		)
		if loadErr != nil {
			return loadErr
		}
		observation, loadErr = loadProvisionDiscoveryTx(
			ctx, tx, req.TenantID, req.OperationID,
			req.ObservationID, req.ObservationSnapshotDigest,
		)
		return loadErr
	})
	return resolutionContext, observation, err
}

func (s *PostgresProvisionResolutionStore) commitProvisionResolution(
	ctx context.Context,
	req ProvisionResolutionRequest,
	expected provisionResolutionContext,
	token string,
) (provisionResolutionCommitResult, error) {
	var result provisionResolutionCommitResult
	err := s.ledger.withTenant(ctx, req.TenantID, func(tx *sql.Tx) error {
		var commitErr error
		result, commitErr = s.commitProvisionResolutionTx(ctx, tx, req, expected, token)
		return commitErr
	})
	return result, err
}

func (s *PostgresProvisionResolutionStore) commitProvisionResolutionTx(
	ctx context.Context,
	tx *sql.Tx,
	req ProvisionResolutionRequest,
	expected provisionResolutionContext,
	token string,
) (provisionResolutionCommitResult, error) {
	if err := lockProvisionResolutionOperationTx(ctx, tx, req.TenantID, req.OperationID); err != nil {
		return provisionResolutionCommitResult{}, err
	}
	if replay, found, err := loadProvisionResolutionReplayTx(ctx, tx, req); err != nil || found {
		return replay, err
	}

	current, decision, next, err := s.prepareProvisionResolutionDecisionTx(ctx, tx, req, expected)
	if err != nil {
		return provisionResolutionCommitResult{}, err
	}
	result, inserted, err := appendProvisionResolutionDecisionTx(ctx, tx, req, decision, token)
	if err != nil || !inserted {
		return result, err
	}
	result.record, err = s.finalizeProvisionResolutionDecisionTx(ctx, tx, current.record, result.decision, next)
	if err != nil {
		return provisionResolutionCommitResult{}, err
	}
	result.created = true
	return result, nil
}

func loadProvisionResolutionReplayTx(
	ctx context.Context,
	tx *sql.Tx,
	req ProvisionResolutionRequest,
) (provisionResolutionCommitResult, bool, error) {
	decision, found, err := loadProvisionDecisionByIdempotencyTx(
		ctx, tx, req.TenantID, req.OperationID, req.IdempotencyKey,
	)
	if err != nil || !found {
		return provisionResolutionCommitResult{}, found, err
	}
	if decision.RequestDigest != req.RequestDigest {
		return provisionResolutionCommitResult{}, false, ErrProvisionResolutionConflict
	}
	record, err := loadOperationProjectionTx(ctx, tx, req.TenantID, req.OperationID, false, false)
	if err != nil {
		return provisionResolutionCommitResult{}, false, err
	}
	return provisionResolutionCommitResult{decision: decision, record: record}, true, nil
}

func (s *PostgresProvisionResolutionStore) prepareProvisionResolutionDecisionTx(
	ctx context.Context,
	tx *sql.Tx,
	req ProvisionResolutionRequest,
	expected provisionResolutionContext,
) (provisionResolutionContext, ProvisionResolutionDecision, *providerexecutor.Receipt, error) {
	current, err := loadProvisionResolutionContextTx(ctx, tx, req.TenantID, req.OperationID)
	if err != nil {
		return provisionResolutionContext{}, ProvisionResolutionDecision{}, nil, err
	}
	if !sameProvisionResolutionContext(current, expected) {
		return provisionResolutionContext{}, ProvisionResolutionDecision{}, nil, ErrProvisionResolutionConflict
	}
	observation, err := loadProvisionDiscoveryTx(
		ctx, tx, req.TenantID, req.OperationID, req.ObservationID, req.ObservationSnapshotDigest,
	)
	if err != nil {
		return provisionResolutionContext{}, ProvisionResolutionDecision{}, nil, err
	}
	if observation.ObservedResolutionRevision != current.binding.ResolutionRevision {
		return provisionResolutionContext{}, ProvisionResolutionDecision{}, nil, ErrProvisionResolutionConflict
	}
	var dbNow time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		return provisionResolutionContext{}, ProvisionResolutionDecision{}, nil,
			fmt.Errorf("providercontrol: read provision resolution database time: %w", err)
	}
	decision, err := deriveProvisionResolutionDecision(current.record, req, observation, dbNow)
	if err != nil {
		return provisionResolutionContext{}, ProvisionResolutionDecision{}, nil, err
	}
	decision, next, err := s.assembleProvisionResolutionReceipt(ctx, current.record, observation, decision, dbNow)
	return current, decision, next, err
}

func (s *PostgresProvisionResolutionStore) assembleProvisionResolutionReceipt(
	ctx context.Context,
	record OperationRecord,
	observation ProvisionDiscoveryObservation,
	decision ProvisionResolutionDecision,
	dbNow time.Time,
) (ProvisionResolutionDecision, *providerexecutor.Receipt, error) {
	if decision.Outcome != ProvisionResolutionAdoptedExactCandidate {
		return decision, nil, nil
	}
	assembled, err := providerexecutor.AssembleReceipt(ctx, providerexecutor.ExecutionRequest{
		Command: record.Command, Previous: record.Head,
	}, providerexecutor.ExecutionResult{
		Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseResourcesBound,
		Resources: observation.Candidates[0].Resources,
	}, dbNow, s.ledger.verifier)
	if err != nil {
		return ProvisionResolutionDecision{}, nil, err
	}
	decision.ResultReceiptSequence = assembled.Sequence
	decision.ResultReceiptDigest = assembled.ReceiptDigest
	decision.DecisionDigest = provisionResolutionDecisionDigest(decision)
	return decision, &assembled, nil
}

func appendProvisionResolutionDecisionTx(
	ctx context.Context,
	tx *sql.Tx,
	req ProvisionResolutionRequest,
	decision ProvisionResolutionDecision,
	token string,
) (provisionResolutionCommitResult, bool, error) {
	if _, err := tx.ExecContext(ctx, `SELECT set_config($1, $2, true)`, provisionResolutionTokenContextKey, token); err != nil {
		return provisionResolutionCommitResult{}, false,
			fmt.Errorf("providercontrol: bind provision resolution transaction: %w", err)
	}
	var decidedAt time.Time
	insertErr := tx.QueryRowContext(ctx, `
		INSERT INTO provider_provision_resolution_decisions (
			tenant_id, operation_id, resolution_revision, observation_id,
			observation_snapshot_digest, expected_head_sequence, expected_head_receipt_digest,
			outcome, selected_candidate_digest, operator_subject_id,
			operator_attestation_ref, operator_attestation_digest, idempotency_key,
			request_digest, decision_digest, result_receipt_sequence,
			result_receipt_digest, decision_token_digest, decided_at
		) VALUES (
			$1,$2,$3,$4::uuid,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,
			clock_timestamp()
		)
		ON CONFLICT (tenant_id, operation_id, idempotency_key) DO NOTHING
		RETURNING decided_at
	`, decision.TenantID, decision.OperationID, int64(decision.ResolutionRevision), // #nosec G115 -- sealProvisionResolutionRequest reserves the bounded increment.
		decision.ObservationID, decision.ObservationSnapshotDigest, int64(decision.ExpectedHeadSequence), // #nosec G115 -- the sealed request bounds the sequence to MaxJSONSafeInteger.
		decision.ExpectedHeadReceiptDigest, decision.Outcome, nullableString(decision.SelectedCandidateDigest),
		decision.OperatorSubjectID, decision.OperatorAttestationRef, decision.OperatorAttestationDigest,
		decision.IdempotencyKey, decision.RequestDigest, decision.DecisionDigest,
		nullableUint64(decision.ResultReceiptSequence), nullableString(decision.ResultReceiptDigest),
		executionClaimTokenDigest(token),
	).Scan(&decidedAt)
	if errors.Is(insertErr, sql.ErrNoRows) {
		replay, found, err := loadProvisionResolutionReplayTx(ctx, tx, req)
		if err != nil {
			return provisionResolutionCommitResult{}, false, err
		}
		if !found {
			return provisionResolutionCommitResult{}, false, ErrProvisionResolutionConflict
		}
		return replay, false, nil
	}
	if insertErr != nil {
		return provisionResolutionCommitResult{}, false,
			fmt.Errorf("providercontrol: append provision resolution decision: %w", insertErr)
	}
	decision.DecidedAt = decidedAt
	return provisionResolutionCommitResult{decision: decision}, true, nil
}

func (s *PostgresProvisionResolutionStore) finalizeProvisionResolutionDecisionTx(
	ctx context.Context,
	tx *sql.Tx,
	record OperationRecord,
	decision ProvisionResolutionDecision,
	next *providerexecutor.Receipt,
) (OperationRecord, error) {
	if isTerminalHandleFreeAMOProvisionHead(record) {
		handled, err := s.ledger.finalizeFailedAMOResourceFreeTeardownTx(ctx, tx, record, decision)
		if err != nil {
			return OperationRecord{}, err
		}
		if !handled {
			return OperationRecord{}, fmt.Errorf(
				"%w: failed AMO zero-candidate decision did not terminalize its exact custody",
				ErrLedgerConflict,
			)
		}
	}
	result := record
	if next != nil {
		nextJSON, err := json.Marshal(next)
		if err != nil {
			return OperationRecord{}, fmt.Errorf("providercontrol: encode adopted provision receipt: %w", err)
		}
		if err := s.ledger.persistAcceptedReceiptTx(
			ctx,
			tx,
			record,
			record.Head,
			*next,
			nextJSON,
			"adopt operator-verified provision candidate",
		); err != nil {
			return OperationRecord{}, err
		}
		result.Head = *next
		result.AutomationState = OperationAutomationRunnable
		result.AutomationReasonCode = ""
	}
	if _, err := tx.ExecContext(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return OperationRecord{}, fmt.Errorf("providercontrol: validate provision resolution constraints: %w", err)
	}
	return result, nil
}

// lockProvisionResolutionOperationTx is the first mutation lock for every
// discovery and decision transaction. Taking it before the operation row lock
// preserves one global lock order across Go and the Migration 031 triggers.
func lockProvisionResolutionOperationTx(
	ctx context.Context,
	tx *sql.Tx,
	tenantID, operationID string,
) error {
	if _, err := tx.ExecContext(ctx, `
		SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))
	`, tenantID, operationID); err != nil {
		return fmt.Errorf("providercontrol: lock provision resolution operation: %w", err)
	}
	return nil
}

func loadProvisionResolutionContextTx(
	ctx context.Context,
	tx *sql.Tx,
	tenantID, operationID string,
) (provisionResolutionContext, error) {
	record, err := loadOperationCustodyTx(ctx, tx, tenantID, operationID, true)
	if err != nil {
		return provisionResolutionContext{}, err
	}
	if err := validateProvisionResolutionHead(record); err != nil {
		return provisionResolutionContext{}, err
	}
	binding := provisionDiscoveryBinding{
		LeaseID: record.Command.LeaseID, LeaseRevision: record.Command.LeaseRevision,
		RuntimeServerID: record.Command.RuntimeServerID, ResourceGenerationID: record.Command.ResourceGenerationID,
	}
	// The operation advisory lock above already serializes resolution, and guard
	// rows are immutable. A row-locking SELECT would additionally require table
	// UPDATE authority and break the dedicated read/insert-only runtime role.
	if err := tx.QueryRowContext(ctx, `
		SELECT head_sequence, head_receipt_digest,
		       adapter_manifest_hash, prepared_request_digest,
		       credential_version_hash, provider_scope_hash, correlation_hash, guarded_at
		FROM provider_provision_dispatch_guards
		WHERE tenant_id = $1 AND operation_id = $2
		  AND dispatch_mode = 'at_most_once_dispatch_manual_reconcile'
		  AND guard_origin = 'first_claim'
	`, tenantID, operationID).Scan(
		&binding.HeadSequence,
		&binding.HeadReceiptDigest,
		&binding.AdapterManifestHash, &binding.PreparedBinding.RequestDigest,
		&binding.PreparedBinding.CredentialVersionHash, &binding.PreparedBinding.ProviderScopeHash,
		&binding.PreparedBinding.CorrelationHash, &binding.GuardedAt,
	); errors.Is(err, sql.ErrNoRows) {
		return provisionResolutionContext{}, ErrProvisionManualReconcile
	} else if err != nil {
		return provisionResolutionContext{}, fmt.Errorf("providercontrol: load provision resolution guard: %w", err)
	}
	if err := validateProvisionResolutionGuard(record, binding); err != nil {
		return provisionResolutionContext{}, err
	}
	binding.PreparedBinding.AdapterManifestHash = binding.AdapterManifestHash
	var terminal bool
	if err := tx.QueryRowContext(ctx, `
		SELECT coalesce(max(resolution_revision), 0),
		       coalesce(bool_or(outcome IN (
		           'adopted_exact_candidate', 'multiple_candidates_quarantined'
		       )), false)
		FROM provider_provision_resolution_decisions
		WHERE tenant_id = $1 AND operation_id = $2
	`, tenantID, operationID).Scan(&binding.ResolutionRevision, &terminal); err != nil {
		return provisionResolutionContext{}, fmt.Errorf("providercontrol: load provision resolution revision: %w", err)
	}
	if terminal {
		return provisionResolutionContext{}, ErrProvisionResolutionConflict
	}
	return provisionResolutionContext{record: record, binding: binding}, nil
}

func loadProvisionDiscoveryTx(
	ctx context.Context,
	tx *sql.Tx,
	tenantID, operationID, observationID, snapshotDigest string,
) (ProvisionDiscoveryObservation, error) {
	query := `
		SELECT observation_id::text, lease_id, lease_revision, server_id,
		       resource_generation_id::text, head_sequence, head_receipt_digest,
		       observed_resolution_revision, adapter_manifest_hash,
		       prepared_request_digest, credential_version_hash, provider_scope_hash,
		       correlation_hash, guarded_at, candidate_graphs_json, observation_ref,
		       observation_digest, attestation_ref, attestation_digest, collected_at,
		       requested_by_subject_id, idempotency_key, request_digest,
		       snapshot_digest, recorded_at
		FROM provider_provision_discovery_observations
		WHERE tenant_id = $1 AND operation_id = $2 AND observation_id = $3::uuid`
	args := []any{tenantID, operationID, observationID}
	if snapshotDigest != "" {
		query += ` AND snapshot_digest = $4`
		args = append(args, snapshotDigest)
	}
	return scanProvisionDiscovery(tx.QueryRowContext(ctx, query, args...), tenantID, operationID)
}

func loadProvisionDiscoveryByIdempotencyTx(
	ctx context.Context,
	tx *sql.Tx,
	tenantID, operationID, idempotencyKey string,
) (ProvisionDiscoveryObservation, bool, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT observation_id::text, lease_id, lease_revision, server_id,
		       resource_generation_id::text, head_sequence, head_receipt_digest,
		       observed_resolution_revision, adapter_manifest_hash,
		       prepared_request_digest, credential_version_hash, provider_scope_hash,
		       correlation_hash, guarded_at, candidate_graphs_json, observation_ref,
		       observation_digest, attestation_ref, attestation_digest, collected_at,
		       requested_by_subject_id, idempotency_key, request_digest,
		       snapshot_digest, recorded_at
		FROM provider_provision_discovery_observations
		WHERE tenant_id = $1 AND operation_id = $2 AND idempotency_key = $3
	`, tenantID, operationID, idempotencyKey)
	observation, err := scanProvisionDiscovery(row, tenantID, operationID)
	if errors.Is(err, ErrOperationNotFound) {
		return ProvisionDiscoveryObservation{}, false, nil
	}
	return observation, err == nil, err
}

type rowScanner interface{ Scan(...any) error }

func scanProvisionDiscovery(row rowScanner, tenantID, operationID string) (ProvisionDiscoveryObservation, error) {
	observation := ProvisionDiscoveryObservation{TenantID: tenantID, OperationID: operationID}
	var leaseRevision, headSequence, resolutionRevision int64
	var candidatesJSON []byte
	err := row.Scan(
		&observation.ObservationID, &observation.LeaseID, &leaseRevision, &observation.RuntimeServerID,
		&observation.ResourceGenerationID, &headSequence, &observation.HeadReceiptDigest,
		&resolutionRevision, &observation.AdapterManifestHash,
		&observation.PreparedBinding.RequestDigest, &observation.PreparedBinding.CredentialVersionHash,
		&observation.PreparedBinding.ProviderScopeHash, &observation.PreparedBinding.CorrelationHash,
		&observation.GuardedAt, &candidatesJSON, &observation.ObservationRef, &observation.ObservationDigest,
		&observation.AttestationRef, &observation.AttestationDigest, &observation.CollectedAt,
		&observation.RequestedBySubjectID, &observation.IdempotencyKey, &observation.RequestDigest,
		&observation.SnapshotDigest, &observation.RecordedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ProvisionDiscoveryObservation{}, ErrOperationNotFound
	}
	if err != nil {
		return ProvisionDiscoveryObservation{}, fmt.Errorf("providercontrol: load provision discovery: %w", err)
	}
	if leaseRevision <= 0 || headSequence <= 0 || resolutionRevision < 0 {
		return ProvisionDiscoveryObservation{}, ErrProvisionResolutionConflict
	}
	observation.LeaseRevision = uint64(leaseRevision)
	observation.HeadSequence = uint64(headSequence)
	observation.ObservedResolutionRevision = uint64(resolutionRevision)
	observation.PreparedBinding.AdapterManifestHash = observation.AdapterManifestHash
	if err := json.Unmarshal(candidatesJSON, &observation.Candidates); err != nil {
		return ProvisionDiscoveryObservation{}, fmt.Errorf("providercontrol: decode provision candidates: %w", err)
	}
	return observation, nil
}

func loadProvisionDecisionByIdempotencyTx(
	ctx context.Context,
	tx *sql.Tx,
	tenantID, operationID, idempotencyKey string,
) (ProvisionResolutionDecision, bool, error) {
	decision := ProvisionResolutionDecision{TenantID: tenantID, OperationID: operationID}
	var revision, headSequence int64
	var selected, resultDigest sql.NullString
	var resultSequence sql.NullInt64
	err := tx.QueryRowContext(ctx, `
		SELECT resolution_revision, observation_id::text, observation_snapshot_digest,
		       expected_head_sequence, expected_head_receipt_digest, outcome,
		       selected_candidate_digest, operator_subject_id, operator_attestation_ref,
		       operator_attestation_digest, idempotency_key, request_digest,
		       decision_digest, result_receipt_sequence, result_receipt_digest, decided_at
		FROM provider_provision_resolution_decisions
		WHERE tenant_id = $1 AND operation_id = $2 AND idempotency_key = $3
	`, tenantID, operationID, idempotencyKey).Scan(
		&revision, &decision.ObservationID, &decision.ObservationSnapshotDigest,
		&headSequence, &decision.ExpectedHeadReceiptDigest, &decision.Outcome,
		&selected, &decision.OperatorSubjectID, &decision.OperatorAttestationRef,
		&decision.OperatorAttestationDigest, &decision.IdempotencyKey, &decision.RequestDigest,
		&decision.DecisionDigest, &resultSequence, &resultDigest, &decision.DecidedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ProvisionResolutionDecision{}, false, nil
	}
	if err != nil {
		return ProvisionResolutionDecision{}, false, fmt.Errorf("providercontrol: load provision resolution decision: %w", err)
	}
	if revision <= 0 || revision > int64(providerexecutor.MaxJSONSafeInteger) ||
		headSequence <= 0 || headSequence > int64(providerexecutor.MaxJSONSafeInteger) ||
		(resultSequence.Valid && (resultSequence.Int64 <= 0 ||
			resultSequence.Int64 > int64(providerexecutor.MaxJSONSafeInteger))) {
		return ProvisionResolutionDecision{}, false, ErrProvisionResolutionConflict
	}
	decision.ResolutionRevision = uint64(revision)
	decision.ExpectedHeadSequence = uint64(headSequence)
	if selected.Valid {
		decision.SelectedCandidateDigest = selected.String
	}
	if resultSequence.Valid {
		// #nosec G115 -- the compound check above bounds a present result sequence to the positive JSON-safe range.
		decision.ResultReceiptSequence = uint64(resultSequence.Int64)
	}
	if resultDigest.Valid {
		decision.ResultReceiptDigest = resultDigest.String
	}
	return decision, true, nil
}

func deterministicProvisionObservationID(req RecordProvisionDiscoveryRequest) string {
	name := strings.Join([]string{req.TenantID, req.OperationID, req.IdempotencyKey}, "\x00")
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String()
}

func sameProvisionResolutionContext(left, right provisionResolutionContext) bool {
	return left.record.Command.CommandDigest == right.record.Command.CommandDigest &&
		left.record.Head.Sequence == right.record.Head.Sequence &&
		left.record.Head.ReceiptDigest == right.record.Head.ReceiptDigest &&
		left.binding.LeaseID == right.binding.LeaseID &&
		left.binding.LeaseRevision == right.binding.LeaseRevision &&
		left.binding.RuntimeServerID == right.binding.RuntimeServerID &&
		left.binding.ResourceGenerationID == right.binding.ResourceGenerationID &&
		left.binding.HeadSequence == right.binding.HeadSequence &&
		left.binding.HeadReceiptDigest == right.binding.HeadReceiptDigest &&
		left.binding.AdapterManifestHash == right.binding.AdapterManifestHash &&
		left.binding.PreparedBinding == right.binding.PreparedBinding &&
		left.binding.GuardedAt.Equal(right.binding.GuardedAt) &&
		left.binding.ResolutionRevision == right.binding.ResolutionRevision
}

func provisionDiscoveryRequestMatches(stored ProvisionDiscoveryObservation, req RecordProvisionDiscoveryRequest) bool {
	return stored.TenantID == strings.TrimSpace(req.TenantID) &&
		stored.OperationID == strings.TrimSpace(req.OperationID) &&
		stored.IdempotencyKey == strings.TrimSpace(req.IdempotencyKey) &&
		stored.RequestDigest == provisionDiscoveryRequestDigest(req)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableUint64(value uint64) any {
	if value == 0 {
		return nil
	}
	// #nosec G115 -- the only caller passes zero or AssembleReceipt's JSON-safe sequence.
	return int64(value)
}

var _ ProvisionResolutionStore = (*PostgresProvisionResolutionStore)(nil)
