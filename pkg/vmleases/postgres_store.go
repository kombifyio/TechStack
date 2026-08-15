package vmleases

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) ExecutionAuthority(ctx context.Context, tenantID string, leaseID vmlease.LeaseID) (LeaseExecutionAuthority, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("vmleases: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	leaseID = vmlease.LeaseID(strings.TrimSpace(string(leaseID)))
	if tenantID == "" {
		return "", ErrTenantRequired
	}
	if leaseID == "" {
		return "", ErrNotFound
	}
	var authority LeaseExecutionAuthority
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			SELECT execution_authority
			FROM runtime_lease_execution_authorities
			WHERE tenant_id = $1 AND lease_id = $2
		`, tenantID, leaseID).Scan(&authority); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrLeaseExecutionAuthorityUnbound
			}
			return err
		}
		return validateLeaseExecutionAuthority(authority)
	})
	if err != nil {
		return "", err
	}
	return normalizeLeaseExecutionAuthority(authority), nil
}

func (s *PostgresStore) Upsert(ctx context.Context, lease vmlease.Lease, idempotencyKey string) (*vmlease.Lease, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("vmleases: database not configured")
	}
	tenantID, err := tenantIDFromLease(lease)
	if err != nil {
		return nil, err
	}
	var out *vmlease.Lease
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		created, upsertErr := upsertLeaseTx(ctx, tx, lease, tenantID, idempotencyKey)
		out = created
		return upsertErr
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) Get(ctx context.Context, tenantID string, id vmlease.LeaseID) (*vmlease.Lease, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("vmleases: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	var out *vmlease.Lease
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var payload []byte
		var desiredState string
		var validFrom, validUntil, renewedAt, cancelledAt sql.NullTime
		err := tx.QueryRowContext(ctx, `
			SELECT lease_json::text, desired_state, valid_from, valid_until, renewed_at, cancelled_at
			FROM techstack_vm_leases
			WHERE tenant_id = $1 AND id = $2
		`, tenantID, id).Scan(&payload, &desiredState, &validFrom, &validUntil, &renewedAt, &cancelledAt)
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		lease, err := decodeCanonicalLease(payload, desiredState, validFrom, validUntil, renewedAt, cancelledAt)
		if err != nil {
			return err
		}
		out = lease
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) getInventory(ctx context.Context, tenantID string, id vmlease.LeaseID) (*leaseInventoryRow, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("vmleases: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	var out *leaseInventoryRow
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var payload []byte
		var desiredState string
		var validFrom, validUntil, renewedAt, cancelledAt sql.NullTime
		var authority sql.NullString
		err := tx.QueryRowContext(ctx, `
			SELECT lease.lease_json::text, lease.desired_state,
			       lease.valid_from, lease.valid_until, lease.renewed_at, lease.cancelled_at,
			       authority.execution_authority
			FROM techstack_vm_leases AS lease
			LEFT JOIN runtime_lease_execution_authorities AS authority
				ON authority.tenant_id = lease.tenant_id AND authority.lease_id = lease.id
			WHERE lease.tenant_id = $1 AND lease.id = $2
		`, tenantID, id).Scan(&payload, &desiredState, &validFrom, &validUntil, &renewedAt, &cancelledAt, &authority)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		lease, decodeErr := decodeCanonicalLease(payload, desiredState, validFrom, validUntil, renewedAt, cancelledAt)
		if decodeErr != nil {
			return decodeErr
		}
		out = &leaseInventoryRow{lease: *lease}
		if authority.Valid {
			out.authority = LeaseExecutionAuthority(authority.String)
			out.authorityBound = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) ListByTenant(ctx context.Context, tenantID string) ([]vmlease.Lease, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("vmleases: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	var out []vmlease.Lease
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT lease_json::text, desired_state, valid_from, valid_until, renewed_at, cancelled_at
			FROM techstack_vm_leases
			WHERE tenant_id = $1
			ORDER BY created_at DESC
		`, tenantID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var payload []byte
			var desiredState string
			var validFrom, validUntil, renewedAt, cancelledAt sql.NullTime
			if err := rows.Scan(&payload, &desiredState, &validFrom, &validUntil, &renewedAt, &cancelledAt); err != nil {
				return err
			}
			lease, err := decodeCanonicalLease(payload, desiredState, validFrom, validUntil, renewedAt, cancelledAt)
			if err != nil {
				return err
			}
			out = append(out, *lease)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) listInventoryByTenant(ctx context.Context, tenantID string) ([]leaseInventoryRow, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("vmleases: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	var out []leaseInventoryRow
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT lease.lease_json::text, lease.desired_state,
			       lease.valid_from, lease.valid_until, lease.renewed_at, lease.cancelled_at,
			       authority.execution_authority
			FROM techstack_vm_leases AS lease
			LEFT JOIN runtime_lease_execution_authorities AS authority
				ON authority.tenant_id = lease.tenant_id AND authority.lease_id = lease.id
			WHERE lease.tenant_id = $1
			ORDER BY lease.created_at DESC
		`, tenantID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var payload []byte
			var desiredState string
			var validFrom, validUntil, renewedAt, cancelledAt sql.NullTime
			var authority sql.NullString
			if err := rows.Scan(&payload, &desiredState, &validFrom, &validUntil, &renewedAt, &cancelledAt, &authority); err != nil {
				return err
			}
			lease, decodeErr := decodeCanonicalLease(payload, desiredState, validFrom, validUntil, renewedAt, cancelledAt)
			if decodeErr != nil {
				return decodeErr
			}
			row := leaseInventoryRow{lease: *lease}
			if authority.Valid {
				row.authority = LeaseExecutionAuthority(authority.String)
				row.authorityBound = true
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) Update(ctx context.Context, tenantID string, lease vmlease.Lease) (*vmlease.Lease, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("vmleases: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	leaseTenantID, tenantErr := tenantIDFromLease(lease)
	if tenantErr != nil {
		return nil, tenantErr
	}
	if leaseTenantID != tenantID {
		return nil, ErrNotFound
	}
	var out *vmlease.Lease
	storeErr := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		var existingPayload []byte
		if err := tx.QueryRowContext(ctx, `
			SELECT lease_json::text
			FROM techstack_vm_leases
			WHERE tenant_id = $1 AND id = $2
			FOR UPDATE
		`, tenantID, lease.ID).Scan(&existingPayload); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		existing, decodeErr := decodeLease(existingPayload)
		if decodeErr != nil {
			return decodeErr
		}
		if generationErr := ensureResourceGenerationUnchanged(*existing, lease); generationErr != nil {
			return generationErr
		}
		if cancellationErr := ensureCancellationMonotonic(*existing, lease); cancellationErr != nil {
			return cancellationErr
		}
		if claimErr := ensureDecommissionClaimUnchanged(*existing, lease); claimErr != nil {
			return claimErr
		}
		payload, err := json.Marshal(lease)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE techstack_vm_leases SET
				desired_state = $2,
				valid_until = $3,
				renewed_at = $4,
				cancelled_at = $5,
				lease_json = $6::jsonb,
				updated_at = now()
			WHERE tenant_id = $7 AND id = $1
		`, lease.ID, postgresDesiredState(lease.DesiredState), lease.ValidUntil, nullableLeaseTime(lease.RenewedAt), lease.CancelledAt, payload, tenantID)
		if err != nil {
			return err
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return ErrNotFound
		}
		copied := lease
		out = &copied
		return nil
	})
	if storeErr != nil {
		return nil, storeErr
	}
	return out, nil
}

func nullableLeaseTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func (s *PostgresStore) AppendOperation(ctx context.Context, event OperationEvent) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("vmleases: database not configured")
	}
	tenantID := strings.TrimSpace(event.TenantID)
	if tenantID == "" {
		return ErrTenantRequired
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO techstack_vm_lease_operation_journal (
				tenant_id, lease_id, event_type, status, actor, error, resource_generation_digest, created_at
			) VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),$8)
		`, tenantID, event.LeaseID, event.EventType, event.Status, strings.TrimSpace(event.Actor), strings.TrimSpace(event.Error), strings.TrimSpace(event.ResourceGenerationDigest), event.CreatedAt)
		return err
	})
}

func (s *PostgresStore) ListOperations(ctx context.Context, tenantID string, leaseID vmlease.LeaseID, limit int) ([]OperationEvent, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("vmleases: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	if limit <= 0 {
		limit = 50
	}
	var out []OperationEvent
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT tenant_id, lease_id, event_type, status, COALESCE(actor, ''), COALESCE(error, ''),
				COALESCE(resource_generation_digest, ''), created_at
			FROM techstack_vm_lease_operation_journal
			WHERE tenant_id = $1 AND lease_id = $2
			ORDER BY created_at DESC
			LIMIT $3
		`, tenantID, leaseID, limit)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var event OperationEvent
			var leaseIDRaw string
			if err := rows.Scan(&event.TenantID, &leaseIDRaw, &event.EventType, &event.Status, &event.Actor, &event.Error, &event.ResourceGenerationDigest, &event.CreatedAt); err != nil {
				return err
			}
			event.LeaseID = vmlease.LeaseID(leaseIDRaw)
			out = append(out, event)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) HasConfirmedDecommission(ctx context.Context, tenantID string, leaseID vmlease.LeaseID, resourceGenerationDigest string) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("vmleases: database not configured")
	}
	tenantID = strings.TrimSpace(tenantID)
	resourceGenerationDigest = strings.TrimSpace(resourceGenerationDigest)
	if tenantID == "" {
		return false, ErrTenantRequired
	}
	if leaseID == "" {
		return false, ErrNotFound
	}
	if !validResourceGenerationDigest(resourceGenerationDigest) {
		return false, ErrResourceGenerationDigest
	}
	var exists bool
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM techstack_vm_lease_operation_journal
				WHERE tenant_id = $1
				  AND lease_id = $2
				  AND event_type = $3
				  AND status = $4
				  AND resource_generation_digest = $5
			)
		`, tenantID, leaseID, OperationEventDecommission, OperationStatusDecommissioned, resourceGenerationDigest).Scan(&exists)
	})
	return exists, err
}

func (s *PostgresStore) withTenant(ctx context.Context, tenantID string, fn func(*sql.Tx) error) error {
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
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		return err
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

func getByIdempotencyKeyTx(ctx context.Context, tx *sql.Tx, tenantID, key string) (*vmlease.Lease, error) {
	var payload []byte
	err := tx.QueryRowContext(ctx, `SELECT lease_json::text FROM techstack_vm_leases WHERE tenant_id = $1 AND idempotency_key = $2`, tenantID, strings.TrimSpace(key)).Scan(&payload)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return decodeLease(payload)
}

func lockLeaseIdempotencyKeyTx(ctx context.Context, tx *sql.Tx, tenantID, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	// A tenant-scoped transaction lock closes the no-row race between the
	// idempotency pre-read and INSERT. Hash collisions only serialize unrelated
	// creates; they cannot merge identities or weaken the unique constraint.
	_, err := tx.ExecContext(ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))`,
		strings.TrimSpace(tenantID), key,
	)
	return err
}

func upsertLeaseTx(ctx context.Context, tx *sql.Tx, lease vmlease.Lease, tenantID, idempotencyKey string) (*vmlease.Lease, error) {
	normalizedIdempotencyKey := strings.TrimSpace(idempotencyKey)
	if normalizedIdempotencyKey != "" {
		if err := lockLeaseIdempotencyKeyTx(ctx, tx, tenantID, normalizedIdempotencyKey); err != nil {
			return nil, fmt.Errorf("lock VM lease idempotency key: %w", err)
		}
		existing, err := getByIdempotencyKeyTx(ctx, tx, tenantID, normalizedIdempotencyKey)
		if err == nil {
			return existing, nil
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	payload, marshalErr := encodeLease(lease)
	if marshalErr != nil {
		return nil, marshalErr
	}
	var insertedID string
	upsertErr := tx.QueryRowContext(ctx, `
		INSERT INTO techstack_vm_leases (
			id, tenant_id, subject_id, org_id, provider_id, engine_vm_id, desired_state,
			idempotency_key, lease_json, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),$9::jsonb,now(),now())
		ON CONFLICT(id) DO NOTHING
		RETURNING id
	`, lease.ID, tenantID, lease.Subject.ID, lease.Subject.OrgID, lease.Resource.ProviderID, lease.Resource.EngineVMID,
		postgresDesiredState(lease.DesiredState), normalizedIdempotencyKey, payload).Scan(&insertedID)
	if errors.Is(upsertErr, sql.ErrNoRows) {
		// Existing inventory is immutable through create. A new native provider
		// operation must use an explicit generation-bound admission instead of
		// replacing a lease based on legacy enrollment metadata.
		var existingPayload []byte
		if err := tx.QueryRowContext(ctx, `
			SELECT lease_json::text
			FROM techstack_vm_leases
			WHERE tenant_id = $1 AND id = $2
			FOR UPDATE
		`, tenantID, lease.ID).Scan(&existingPayload); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrLeaseIdentityConflict
			}
			return nil, err
		}
		existing, decodeErr := decodeLease(existingPayload)
		return existing, decodeErr
	}
	if upsertErr != nil {
		return nil, upsertErr
	}
	copied := lease
	return &copied, nil
}

func decodeLease(payload []byte) (*vmlease.Lease, error) {
	var lease vmlease.Lease
	if err := json.Unmarshal(payload, &lease); err != nil {
		return nil, err
	}
	return &lease, nil
}

func decodeCanonicalLease(
	payload []byte,
	desiredState string,
	validFrom, validUntil, renewedAt, cancelledAt sql.NullTime,
) (*vmlease.Lease, error) {
	lease, err := decodeLease(payload)
	if err != nil {
		return nil, err
	}
	switch strings.TrimSpace(desiredState) {
	case "running":
		lease.DesiredState = vmlease.DesiredStateRunning
	case "stopped":
		lease.DesiredState = vmlease.DesiredStateStopped
	case "absent":
		lease.DesiredState = vmlease.DesiredStateArchived
	default:
		return nil, fmt.Errorf("vmleases: unsupported canonical desired state %q", desiredState)
	}
	if validFrom.Valid {
		lease.ValidFrom = validFrom.Time.UTC()
	}
	if validUntil.Valid {
		lease.ValidUntil = validUntil.Time.UTC()
	}
	if renewedAt.Valid {
		lease.RenewedAt = renewedAt.Time.UTC()
	}
	lease.CancelledAt = nil
	if cancelledAt.Valid {
		value := cancelledAt.Time.UTC()
		lease.CancelledAt = &value
	}
	return lease, nil
}

// postgresDesiredState projects the legacy product-model archival name onto
// the lifecycle vocabulary introduced by migration 026. The JSON document
// remains "archived" for API compatibility; the indexed PostgreSQL column is
// authoritative for native lifecycle queries and uses "absent".
func postgresDesiredState(state vmlease.DesiredState) string {
	if state == vmlease.DesiredStateArchived {
		return "absent"
	}
	return string(state)
}

func tenantIDFromLease(lease vmlease.Lease) (string, error) {
	tenantID := strings.TrimSpace(lease.Subject.OrgID)
	if tenantID == "" {
		return "", ErrTenantRequired
	}
	return tenantID, nil
}
