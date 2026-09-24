package providercontrol

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	managedRuntimeCapacityScopeOwner = "owner_subject"
	managedRuntimeCapacityOrigin     = "native_admission"
	managedRuntimeCapacityDigestV2   = "providercontrol/managed-runtime-capacity-policy/v2"
	managedRuntimeCapacityMaxLimit   = 2147483647

	// CapacityDecisionSourceSignedRuntimeBudget and
	// CapacityDecisionSourceSelfHostManifest are the only native policy
	// authorities accepted at the durable provider-control boundary. Keeping
	// this set closed prevents an arbitrary resolver label from becoming an
	// executable capacity grant.
	CapacityDecisionSourceSignedRuntimeBudget = "edge_v2_entitlement+signed_budget:cloud.runtime.credits#managed_servers"
	CapacityDecisionSourceSelfHostManifest    = "static_release_manifest:selfhost-oss"
)

var managedRuntimeProviderIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// CapacityMode is an explicit product-policy decision. An empty mode or a
// zero limit never means unlimited.
type CapacityMode string

const (
	CapacityModeLimited   CapacityMode = "limited"
	CapacityModeUnlimited CapacityMode = "unlimited"
)

// CapacityPolicyRequest identifies the product entitlement subject whose
// managed-runtime capacity must be reserved. It contains no provider
// credential or execution-contract data.
type CapacityPolicyRequest struct {
	TenantID       string
	OwnerSubjectID string
	ProviderID     string
}

// CapacityGrant is a service-owned product authorization decision. The
// resolver is construction-fixed on NativeAdmission; callers cannot select
// their own limit or unlimited mode through NativeProvisionAdmissionRequest.
type CapacityGrant struct {
	ScopeKind      string
	ScopeID        string
	Mode           CapacityMode
	Limit          int
	DecisionSource string
}

// CapacityPolicyResolver resolves fresh managed-runtime admissions. Exact
// durable replays are intentionally returned before this mutable policy is
// consulted.
type CapacityPolicyResolver interface {
	ResolveCapacity(context.Context, CapacityPolicyRequest) (CapacityGrant, error)
}

var (
	// ErrManagedRuntimeCapacityExceeded is returned before any native aggregate
	// write when the exact owner-scoped reservation ceiling is full.
	ErrManagedRuntimeCapacityExceeded = errors.New("providercontrol: managed runtime capacity exceeded")
	// ErrManagedRuntimeCapacityPolicyUnavailable identifies a missing, invalid,
	// or temporarily unavailable service-owned product policy.
	ErrManagedRuntimeCapacityPolicyUnavailable = errors.New("providercontrol: managed runtime capacity policy unavailable")
)

// ManagedRuntimeCapacityExceededError exposes the bounded decision needed for
// a truthful API denial without weakening the underlying fail-closed check.
type ManagedRuntimeCapacityExceededError struct {
	TenantID       string
	OwnerSubjectID string
	Limit          int
	Held           int
}

func (e *ManagedRuntimeCapacityExceededError) Error() string {
	if e == nil {
		return ErrManagedRuntimeCapacityExceeded.Error()
	}
	return fmt.Sprintf("%s: %d of %d slots are reserved", ErrManagedRuntimeCapacityExceeded, e.Held, e.Limit)
}

func (*ManagedRuntimeCapacityExceededError) Unwrap() error {
	return ErrManagedRuntimeCapacityExceeded
}

type normalizedCapacityGrant struct {
	CapacityGrant
	PolicyDigest string
}

func resolveNativeAdmissionCapacity(
	ctx context.Context,
	resolver CapacityPolicyResolver,
	request NativeProvisionAdmissionRequest,
) (normalizedCapacityGrant, error) {
	if nilProviderControlInterface(resolver) {
		return normalizedCapacityGrant{}, fmt.Errorf("%w: resolver is not configured", ErrManagedRuntimeCapacityPolicyUnavailable)
	}
	grant, err := resolver.ResolveCapacity(ctx, CapacityPolicyRequest{
		TenantID: request.TenantID, OwnerSubjectID: request.OwnerSubjectID,
		ProviderID: request.Lease.Resource.ProviderID,
	})
	if err != nil {
		return normalizedCapacityGrant{}, fmt.Errorf("%w: %w", ErrManagedRuntimeCapacityPolicyUnavailable, err)
	}
	grant.ScopeKind = strings.ToLower(strings.TrimSpace(grant.ScopeKind))
	grant.ScopeID = strings.TrimSpace(grant.ScopeID)
	grant.DecisionSource = strings.TrimSpace(grant.DecisionSource)
	providerID := strings.ToLower(strings.TrimSpace(request.Lease.Resource.ProviderID))
	if grant.ScopeKind != managedRuntimeCapacityScopeOwner ||
		grant.ScopeID != request.OwnerSubjectID || !validManagedRuntimeProviderID(providerID) ||
		!validNativeCapacityDecisionSource(grant.DecisionSource) {
		return normalizedCapacityGrant{}, fmt.Errorf("%w: capacity scope or decision source is invalid", ErrManagedRuntimeCapacityPolicyUnavailable)
	}
	switch grant.Mode {
	case CapacityModeLimited:
		if grant.Limit <= 0 || grant.Limit > managedRuntimeCapacityMaxLimit {
			return normalizedCapacityGrant{}, fmt.Errorf("%w: limited capacity requires a supported positive limit", ErrManagedRuntimeCapacityPolicyUnavailable)
		}
	case CapacityModeUnlimited:
		if grant.Limit != 0 {
			return normalizedCapacityGrant{}, fmt.Errorf("%w: unlimited capacity cannot carry a numeric limit", ErrManagedRuntimeCapacityPolicyUnavailable)
		}
	default:
		return normalizedCapacityGrant{}, fmt.Errorf("%w: explicit limited or unlimited mode is required", ErrManagedRuntimeCapacityPolicyUnavailable)
	}
	return normalizedCapacityGrant{
		CapacityGrant: grant,
		PolicyDigest: managedRuntimeCapacityPolicyDigest(
			request.TenantID, providerID, grant.ScopeKind, grant.ScopeID,
			grant.Mode, grant.Limit, grant.DecisionSource,
		),
	}, nil
}

func lockManagedRuntimeCapacityTx(ctx context.Context, tx *sql.Tx, tenantID string, grant normalizedCapacityGrant) error {
	_, err := tx.ExecContext(
		ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))`,
		tenantID,
		"providercontrol.capacity:"+grant.ScopeKind+":"+grant.ScopeID,
	)
	if err != nil {
		return fmt.Errorf("providercontrol: lock managed runtime capacity: %w", err)
	}
	return nil
}

func requireManagedRuntimeCapacityTx(
	ctx context.Context,
	tx *sql.Tx,
	tenantID string,
	grant normalizedCapacityGrant,
) error {
	if grant.Mode == CapacityModeUnlimited {
		return nil
	}
	var held int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*)
		FROM managed_runtime_capacity_reservations AS reservation
		WHERE reservation.tenant_id = $1
		  AND reservation.owner_subject_id = $2
		  AND NOT EXISTS (
		      SELECT 1
		      FROM managed_runtime_capacity_release_facts AS release
		      WHERE release.tenant_id = reservation.tenant_id
		        AND release.lease_id = reservation.lease_id
		        AND release.resource_generation_id = reservation.resource_generation_id
		  )
		  AND NOT EXISTS (
		      SELECT 1
		      FROM managed_runtime_capacity_quarantine_retirements AS retirement
		      WHERE retirement.tenant_id = reservation.tenant_id
		        AND retirement.lease_id = reservation.lease_id
		        AND retirement.resource_generation_id = reservation.resource_generation_id
		  )
	`, tenantID, grant.ScopeID).Scan(&held); err != nil {
		return fmt.Errorf("providercontrol: count managed runtime capacity: %w", err)
	}
	if held >= grant.Limit {
		return &ManagedRuntimeCapacityExceededError{
			TenantID: tenantID, OwnerSubjectID: grant.ScopeID,
			Limit: grant.Limit, Held: held,
		}
	}
	return nil
}

func insertManagedRuntimeCapacityReservationTx(
	ctx context.Context,
	tx *sql.Tx,
	request NativeProvisionAdmissionRequest,
	result NativeProvisionAdmissionResult,
	grant normalizedCapacityGrant,
	reservedAt time.Time,
) error {
	var limit any
	if grant.Mode == CapacityModeLimited {
		limit = grant.Limit
	}
	insert, err := tx.ExecContext(ctx, `
		INSERT INTO managed_runtime_capacity_reservations (
			tenant_id, owner_subject_id, provider_id, lease_id, resource_generation_id,
			operation_id, admission_idempotency_key, reservation_mode,
			capacity_limit, reservation_origin, policy_source, policy_digest, reserved_at
		) VALUES ($1,$2,$3,$4,$5::uuid,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT DO NOTHING
	`,
		request.TenantID, grant.ScopeID, strings.ToLower(strings.TrimSpace(request.Lease.Resource.ProviderID)),
		result.LeaseID, result.ResourceGenerationID,
		result.Operation.Command.OperationID, request.IdempotencyKey, grant.Mode, limit, managedRuntimeCapacityOrigin,
		grant.DecisionSource, grant.PolicyDigest, reservedAt,
	)
	if err != nil {
		return fmt.Errorf("providercontrol: reserve managed runtime capacity: %w", err)
	}
	rows, err := insert.RowsAffected()
	if err != nil || rows != 1 {
		return fmt.Errorf("%w: managed runtime capacity reservation already exists", ErrNativeAdmissionConflict)
	}
	return nil
}

func validateManagedRuntimeCapacityReplayTx(
	ctx context.Context,
	tx *sql.Tx,
	request NativeProvisionAdmissionRequest,
	result NativeProvisionAdmissionResult,
) error {
	var replay managedRuntimeCapacityReplay
	err := tx.QueryRowContext(ctx, `
		SELECT owner_subject_id, provider_id, resource_generation_id::text, operation_id,
		       reservation_mode, capacity_limit, reservation_origin,
		       policy_source, policy_digest
		FROM managed_runtime_capacity_reservations
		WHERE tenant_id = $1 AND lease_id = $2 AND resource_generation_id = $3::uuid
	`, request.TenantID, result.LeaseID, result.ResourceGenerationID).Scan(
		&replay.ownerID, &replay.providerID, &replay.generationID, &replay.operationID,
		&replay.mode, &replay.capacityLimit, &replay.origin, &replay.policySource, &replay.policyDigest,
	)
	if err != nil {
		return fmt.Errorf("%w: load managed runtime capacity replay: %v", ErrNativeAdmissionConflict, err)
	}
	if !replay.matches(request, result) {
		return fmt.Errorf("%w: managed runtime capacity replay projection mismatch", ErrNativeAdmissionConflict)
	}
	return nil
}

type managedRuntimeCapacityReplay struct {
	ownerID       string
	providerID    string
	generationID  string
	operationID   sql.NullString
	mode          string
	capacityLimit sql.NullInt64
	origin        string
	policySource  string
	policyDigest  string
}

func (replay managedRuntimeCapacityReplay) matches(
	request NativeProvisionAdmissionRequest,
	result NativeProvisionAdmissionResult,
) bool {
	canonicalProviderID := strings.ToLower(strings.TrimSpace(request.Lease.Resource.ProviderID))
	return replay.ownerID == request.OwnerSubjectID && replay.providerID == canonicalProviderID &&
		validManagedRuntimeProviderID(replay.providerID) && replay.generationID == result.ResourceGenerationID &&
		replay.operationID.Valid && replay.operationID.String == result.Operation.Command.OperationID &&
		replay.origin == managedRuntimeCapacityOrigin && validNativeCapacityDecisionSource(replay.policySource) &&
		replay.modeMatchesLimit() && validCapacityPolicyDigest(replay.policyDigest) &&
		replay.policyDigest == replay.expectedPolicyDigest(request.TenantID)
}

func (replay managedRuntimeCapacityReplay) modeMatchesLimit() bool {
	return (replay.mode == string(CapacityModeLimited) && replay.capacityLimit.Valid && replay.capacityLimit.Int64 > 0) ||
		(replay.mode == string(CapacityModeUnlimited) && !replay.capacityLimit.Valid)
}

func (replay managedRuntimeCapacityReplay) expectedPolicyDigest(tenantID string) string {
	limit := 0
	if replay.capacityLimit.Valid {
		limit = int(replay.capacityLimit.Int64)
	}
	return managedRuntimeCapacityPolicyDigest(
		tenantID, replay.providerID, managedRuntimeCapacityScopeOwner, replay.ownerID,
		CapacityMode(replay.mode), limit, replay.policySource,
	)
}

func managedRuntimeCapacityPolicyDigest(
	tenantID, providerID, scopeKind, scopeID string,
	mode CapacityMode,
	limit int,
	decisionSource string,
) string {
	var payload strings.Builder
	for _, value := range []string{
		managedRuntimeCapacityDigestV2,
		tenantID,
		providerID,
		scopeKind,
		scopeID,
		string(mode),
		strconv.Itoa(limit),
		decisionSource,
	} {
		_, _ = fmt.Fprintf(&payload, "%d:%s\n", len([]byte(value)), value)
	}
	digest := sha256.Sum256([]byte(payload.String()))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validManagedRuntimeProviderID(value string) bool {
	return managedRuntimeProviderIDPattern.MatchString(value)
}

func validNativeCapacityDecisionSource(value string) bool {
	switch value {
	case CapacityDecisionSourceSignedRuntimeBudget, CapacityDecisionSourceSelfHostManifest:
		return true
	default:
		return false
	}
}

func validCapacityPolicyDigest(value string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil && len(decoded) == sha256.Size
}
