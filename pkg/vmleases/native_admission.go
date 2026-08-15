package vmleases

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

const (
	// NativeAdmissionLeaseRevision is the first revision of a Lease created by
	// native Provider Execution Authority admission.
	NativeAdmissionLeaseRevision = uint64(1)
	// DefaultNativeAdmissionValidity is the database-time validity window used
	// when a native admission supplies no duration.
	DefaultNativeAdmissionValidity = 30 * 24 * time.Hour
	// MaxNativeAdmissionValidity bounds one admitted lease validity window.
	MaxNativeAdmissionValidity = 366 * 24 * time.Hour
)

// NativeAdmissionRequest contains only the Lease custody facts needed inside a
// caller-owned native provider admission transaction.
type NativeAdmissionRequest struct {
	Lease          vmlease.Lease
	OwnerSubjectID string
	ServerID       string
	IdempotencyKey string
	ValidFor       time.Duration
}

// NativeAdmissionResult returns the authority-owned facts which the caller must
// use for every other record in the same admission transaction.
type NativeAdmissionResult struct {
	Lease                vmlease.Lease
	ResourceGenerationID string
	LeaseRevision        uint64
	DatabaseTime         time.Time
}

// AdmitNativeLeaseTx creates a fresh Lease and Resource Generation inside a
// caller-owned transaction. It sets tenant context but never commits or rolls
// back the transaction.
func (s *PostgresStore) AdmitNativeLeaseTx(
	ctx context.Context,
	tx *sql.Tx,
	request NativeAdmissionRequest,
) (NativeAdmissionResult, error) {
	if s == nil || s.db == nil || tx == nil {
		return NativeAdmissionResult{}, fmt.Errorf("vmleases: database and transaction required")
	}
	tenantID, tenantErr := tenantIDFromLease(request.Lease)
	if tenantErr != nil {
		return NativeAdmissionResult{}, tenantErr
	}
	ownerSubjectID := strings.TrimSpace(request.OwnerSubjectID)
	serverID := strings.TrimSpace(request.ServerID)
	if ownerSubjectID == "" || serverID == "" {
		return NativeAdmissionResult{}, fmt.Errorf("vmleases: native admission owner and server required")
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		return NativeAdmissionResult{}, fmt.Errorf("vmleases: set native admission tenant context: %w", err)
	}
	var databaseTime time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&databaseTime); err != nil {
		return NativeAdmissionResult{}, fmt.Errorf("vmleases: read native admission database time: %w", err)
	}
	databaseTime = databaseTime.UTC()
	lease, generationID, prepareErr := prepareNativeAdmissionLease(request.Lease, databaseTime, request.ValidFor)
	if prepareErr != nil {
		return NativeAdmissionResult{}, prepareErr
	}
	if _, contractErr := RuntimeContract(lease, NativeAdmissionLeaseRevision, serverID, databaseTime); contractErr != nil {
		return NativeAdmissionResult{}, contractErr
	}
	payload, marshalErr := encodeLease(lease)
	if marshalErr != nil {
		return NativeAdmissionResult{}, fmt.Errorf("vmleases: encode native admission lease: %w", marshalErr)
	}
	result, insertErr := tx.ExecContext(ctx, `
		INSERT INTO techstack_vm_leases (
			id, tenant_id, subject_id, org_id, provider_id, engine_vm_id,
			desired_state, idempotency_key, lease_json, lease_revision,
			owner_subject_id, server_id, resource_generation_id,
			valid_from, valid_until, renewed_at, cancelled_at, created_at, updated_at
		) VALUES (
			$1,$2,$3,NULLIF($4,''),$5,NULL,$6,$7,$8::jsonb,$9,$10,$11,$12::uuid,
			$13,$14,$15,NULL,$16,$16
		)
		ON CONFLICT DO NOTHING
	`,
		lease.ID, tenantID, lease.Subject.ID, lease.Subject.OrgID,
		lease.Resource.ProviderID, lease.DesiredState, strings.TrimSpace(request.IdempotencyKey),
		payload, int64(NativeAdmissionLeaseRevision), ownerSubjectID,
		serverID, generationID, lease.ValidFrom, lease.ValidUntil,
		lease.RenewedAt, databaseTime,
	)
	if insertErr != nil {
		return NativeAdmissionResult{}, fmt.Errorf("vmleases: insert native admission lease: %w", insertErr)
	}
	rows, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		return NativeAdmissionResult{}, fmt.Errorf("vmleases: inspect native admission lease insert: %w", rowsErr)
	}
	if rows != 1 {
		return NativeAdmissionResult{}, ErrLeaseIdentityConflict
	}
	return NativeAdmissionResult{
		Lease:                lease,
		ResourceGenerationID: generationID,
		LeaseRevision:        NativeAdmissionLeaseRevision,
		DatabaseTime:         databaseTime,
	}, nil
}

// PrepareNativeAdmissionLease preserves the pre-existing preparation API.
// New persistence callers should use PostgresStore.CreateNativeAdmission.
func PrepareNativeAdmissionLease(lease vmlease.Lease, now time.Time, validFor time.Duration) (vmlease.Lease, string, error) {
	return prepareNativeAdmissionLease(lease, now, validFor)
}

func prepareNativeAdmissionLease(lease vmlease.Lease, now time.Time, validFor time.Duration) (vmlease.Lease, string, error) {
	lease = cloneLease(lease)
	lease.ID = vmlease.LeaseID(strings.TrimSpace(string(lease.ID)))
	lease.Subject.ID = strings.TrimSpace(lease.Subject.ID)
	lease.Subject.OrgID = strings.TrimSpace(lease.Subject.OrgID)
	lease.Resource.ProviderID = strings.ToLower(strings.TrimSpace(lease.Resource.ProviderID))
	if ResourceGenerationID(lease) != "" {
		return vmlease.Lease{}, "", ErrResourceGenerationImmutable
	}
	if strings.TrimSpace(lease.Resource.EngineVMID) != "" ||
		strings.TrimSpace(lease.Resource.SimulationID) != "" ||
		strings.TrimSpace(lease.Resource.VMID) != "" {
		return vmlease.Lease{}, "", ErrProviderRefRequired
	}
	if now.IsZero() {
		return vmlease.Lease{}, "", fmt.Errorf("%w: database time required", ErrResourceGenerationUnavailable)
	}
	now = now.UTC()
	if validFor == 0 {
		validFor = DefaultNativeAdmissionValidity
	}
	if validFor < time.Minute || validFor > MaxNativeAdmissionValidity {
		return vmlease.Lease{}, "", fmt.Errorf("%w: invalid native lease validity", ErrResourceGenerationUnavailable)
	}
	if !lease.ValidFrom.IsZero() || !lease.ValidUntil.IsZero() ||
		!lease.RenewedAt.IsZero() || lease.CancelledAt != nil {
		return vmlease.Lease{}, "", fmt.Errorf("%w: lease authority times must be unset", ErrResourceGenerationUnavailable)
	}
	lease.ValidFrom = now
	lease.ValidUntil = now.Add(validFor)
	lease.RenewedAt = now
	if err := assignNewResourceGenerationID(&lease); err != nil {
		return vmlease.Lease{}, "", err
	}
	generationID := ResourceGenerationID(lease)
	if err := lease.Validate(now); err != nil {
		return vmlease.Lease{}, "", err
	}
	return lease, generationID, nil
}

func encodeLease(lease vmlease.Lease) ([]byte, error) {
	payload, err := json.Marshal(lease)
	if err != nil {
		return nil, err
	}
	return payload, nil
}
