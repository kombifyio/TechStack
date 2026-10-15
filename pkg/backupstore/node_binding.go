package backupstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrRecipientSubstituted reports that a node offered a recipient key other than
// the one pinned at enrollment. Re-enrollment is an explicit, separate act.
var ErrRecipientSubstituted = errors.New("managed backup recipient differs from the pinned enrollment")

// ErrNodeBindingMissing reports that the node has issued no binding yet.
var ErrNodeBindingMissing = errors.New("managed backup node binding is not recorded")

// RecipientPin is the node's sealed-delivery recipient as enrolled: the owner
// key that signed the statement, the recipient key's fingerprint and the
// generation sealing binds to.
type RecipientPin struct {
	TenantID    string
	StackID     string
	OwnerKey    string
	OwnerKeyID  string
	Fingerprint string
	Generation  uint64
	SiteRef     string
	NodeRef     string
	PinnedAt    time.Time
}

// NodeBinding holds the opaque references the node issued for its own custody.
// Core compares against them and never derives them.
type NodeBinding struct {
	BackupTargetRef       string
	CustodyAttestationRef string
	RecordedAt            time.Time
}

// PinRecipient records the node's recipient at first enrollment. An identical
// repeat is accepted; any other recipient for the same stack is refused.
func (s *PostgresCustodyStore) PinRecipient(ctx context.Context, pin RecipientPin) error {
	if pin.TenantID == "" || pin.StackID == "" || pin.OwnerKey == "" || pin.OwnerKeyID == "" || pin.Fingerprint == "" || pin.Generation == 0 || pin.SiteRef == "" || pin.NodeRef == "" {
		return fmt.Errorf("%w: a complete recipient pin is required", ErrInvalidCustody)
	}
	return s.withTenant(ctx, pin.TenantID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE stack_backup_stores
			SET recipient_owner_public_key = $3, recipient_owner_key_id = $4, recipient_fingerprint = $5,
				recipient_generation = $6, recipient_site_ref = $7, recipient_node_ref = $8, recipient_pinned_at = COALESCE(recipient_pinned_at, now())
			WHERE tenant_id = $1 AND stack_id = $2 AND token_revoked_at IS NULL
			  AND (recipient_fingerprint IS NULL
			       OR (recipient_owner_public_key = $3 AND recipient_owner_key_id = $4 AND recipient_fingerprint = $5
			           AND recipient_generation = $6 AND recipient_site_ref = $7 AND recipient_node_ref = $8))
		`, pin.TenantID, pin.StackID, pin.OwnerKey, pin.OwnerKeyID, pin.Fingerprint, int64(pin.Generation), pin.SiteRef, pin.NodeRef) // #nosec G115 -- generation is a small counter.
		if err != nil {
			return err
		}
		if changed, _ := result.RowsAffected(); changed == 1 {
			return nil
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT true FROM stack_backup_stores WHERE tenant_id = $1 AND stack_id = $2 AND token_revoked_at IS NULL`, pin.TenantID, pin.StackID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrCustodyNotFound
		} else if err != nil {
			return err
		}
		return ErrRecipientSubstituted
	})
}

// Recipient returns the pinned recipient, if the node has been enrolled.
func (s *PostgresCustodyStore) Recipient(ctx context.Context, tenantID, stackID string) (RecipientPin, bool, error) {
	tenantID, stackID = strings.TrimSpace(tenantID), strings.TrimSpace(stackID)
	if tenantID == "" || stackID == "" {
		return RecipientPin{}, false, fmt.Errorf("%w: tenant id and stack id are required", ErrInvalidCustody)
	}
	var pin RecipientPin
	var key, keyID, fingerprint, site, node sql.NullString
	var generation sql.NullInt64
	var pinned sql.NullTime
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `
			SELECT recipient_owner_public_key, recipient_owner_key_id, recipient_fingerprint, recipient_generation,
				recipient_site_ref, recipient_node_ref, recipient_pinned_at
			FROM stack_backup_stores WHERE tenant_id = $1 AND stack_id = $2 AND token_revoked_at IS NULL
		`, tenantID, stackID).Scan(&key, &keyID, &fingerprint, &generation, &site, &node, &pinned)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCustodyNotFound
		}
		return err
	})
	if err != nil {
		return RecipientPin{}, false, err
	}
	if !fingerprint.Valid {
		return RecipientPin{}, false, nil
	}
	pin = RecipientPin{TenantID: tenantID, StackID: stackID, OwnerKey: key.String, OwnerKeyID: keyID.String, Fingerprint: fingerprint.String,
		Generation: uint64(generation.Int64), SiteRef: site.String, NodeRef: node.String, PinnedAt: pinned.Time} // #nosec G115 -- constrained positive by the table.
	return pin, true, nil
}

// RecordNodeBinding stores the references the node issued. A second record for
// the same stack must name the same target and custody: the node refuses a
// replacement, so a different value is a defect, not an update.
func (s *PostgresCustodyStore) RecordNodeBinding(ctx context.Context, tenantID, stackID string, binding NodeBinding) error {
	tenantID, stackID = strings.TrimSpace(tenantID), strings.TrimSpace(stackID)
	if tenantID == "" || stackID == "" || binding.BackupTargetRef == "" || binding.CustodyAttestationRef == "" {
		return fmt.Errorf("%w: a complete node binding is required", ErrInvalidCustody)
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE stack_backup_stores
			SET node_backup_target_ref = $3, node_custody_attestation_ref = $4, node_binding_recorded_at = COALESCE(node_binding_recorded_at, now())
			WHERE tenant_id = $1 AND stack_id = $2 AND token_revoked_at IS NULL AND recipient_fingerprint IS NOT NULL
			  AND (node_backup_target_ref IS NULL OR (node_backup_target_ref = $3 AND node_custody_attestation_ref = $4))
		`, tenantID, stackID, binding.BackupTargetRef, binding.CustodyAttestationRef)
		if err != nil {
			return err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return errors.New("managed backup node binding differs from the one already recorded or the recipient is not enrolled")
		}
		return nil
	})
}

// NodeBinding returns the references the node issued for its own custody.
func (s *PostgresCustodyStore) NodeBinding(ctx context.Context, tenantID, stackID string) (NodeBinding, error) {
	tenantID, stackID = strings.TrimSpace(tenantID), strings.TrimSpace(stackID)
	if tenantID == "" || stackID == "" {
		return NodeBinding{}, fmt.Errorf("%w: tenant id and stack id are required", ErrInvalidCustody)
	}
	var target, custody sql.NullString
	var recorded sql.NullTime
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `
			SELECT node_backup_target_ref, node_custody_attestation_ref, node_binding_recorded_at
			FROM stack_backup_stores WHERE tenant_id = $1 AND stack_id = $2 AND token_revoked_at IS NULL
		`, tenantID, stackID).Scan(&target, &custody, &recorded)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCustodyNotFound
		}
		return err
	})
	if err != nil {
		return NodeBinding{}, err
	}
	if !target.Valid {
		return NodeBinding{}, ErrNodeBindingMissing
	}
	return NodeBinding{BackupTargetRef: target.String, CustodyAttestationRef: custody.String, RecordedAt: recorded.Time}, nil
}
