package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ReadStackIdentitySnapshot reads the owner revision and active identity from
// one repeatable-read snapshot. A deleted homelab can leave a nonzero revision;
// a later replacement cannot reuse an old ETag.
func (s *PostgresStore) ReadStackIdentitySnapshot(ctx context.Context, tenantID, ownerSubjectID string) (*Homelab, int64, error) {
	if s == nil || s.db == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(ownerSubjectID) == "" {
		return nil, 0, fmt.Errorf("controlplane: tenant and owner required")
	}
	tenantID, ownerSubjectID = strings.TrimSpace(tenantID), strings.TrimSpace(ownerSubjectID)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SELECT set_config($1, $2, true)", tenantGUC, tenantID); err != nil {
		return nil, 0, err
	}
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT revision FROM homelab_identity_revisions
		WHERE tenant_id = $1 AND owner_subject_id = $2`, tenantID, ownerSubjectID).Scan(&revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, 0, err
	}
	homelab, err := selectHomelabByOwner(ctx, tx, tenantID, ownerSubjectID)
	if errors.Is(err, ErrNotFound) {
		homelab, err = nil, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	return homelab, revision, nil
}

// lockStackIdentityRevision is the first write lock taken by every identity
// writer. It serializes compatibility edits, Cloud sync and conditional edits.
func lockStackIdentityRevision(ctx context.Context, tx *sql.Tx, tenantID, ownerSubjectID string) (int64, error) {
	if _, err := tx.ExecContext(ctx, `INSERT INTO homelab_identity_revisions (tenant_id, owner_subject_id)
		VALUES ($1, $2) ON CONFLICT DO NOTHING`, tenantID, ownerSubjectID); err != nil {
		return 0, err
	}
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT revision FROM homelab_identity_revisions
		WHERE tenant_id = $1 AND owner_subject_id = $2 FOR UPDATE`, tenantID, ownerSubjectID).Scan(&revision)
	return revision, err
}

func lockHomelabIdentityRevision(ctx context.Context, tx *sql.Tx, tenantID, homelabID string) (string, int64, error) {
	var ownerSubjectID string
	err := tx.QueryRowContext(ctx, `SELECT owner_subject_id FROM homelabs
		WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`, tenantID, homelabID).Scan(&ownerSubjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, ErrNotFound
	}
	if err != nil {
		return "", 0, err
	}
	revision, err := lockStackIdentityRevision(ctx, tx, tenantID, ownerSubjectID)
	return ownerSubjectID, revision, err
}

func advanceStackIdentityRevision(ctx context.Context, tx *sql.Tx, tenantID, ownerSubjectID string) (int64, error) {
	var revision int64
	err := tx.QueryRowContext(ctx, `UPDATE homelab_identity_revisions SET revision = revision + 1
		WHERE tenant_id = $1 AND owner_subject_id = $2 RETURNING revision`, tenantID, ownerSubjectID).Scan(&revision)
	return revision, err
}

// ApplyStackIdentityMutation returns the stored original result on a repeated
// key, even when the caller's old If-Match is now stale. The owner revision row
// locks before any homelab row, matching the compatibility writers' order.
func (s *PostgresStore) ApplyStackIdentityMutation(ctx context.Context, input StackIdentityMutation) (*StackIdentityMutationResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	input.TenantID = strings.TrimSpace(input.TenantID)
	input.OwnerSubjectID = strings.TrimSpace(input.OwnerSubjectID)
	input.HomelabID = strings.TrimSpace(input.HomelabID)
	input.Write.Name = strings.TrimSpace(input.Write.Name)
	if input.TenantID == "" || input.OwnerSubjectID == "" || input.HomelabID == "" ||
		input.Write.Name == "" || input.ExpectedRevision < 0 ||
		len(input.MutationID) < 16 || len(input.MutationID) > 128 || len(input.PayloadSHA256) != 64 {
		return nil, fmt.Errorf("controlplane: invalid stack identity mutation")
	}
	var result *StackIdentityMutationResult
	err := s.withTenant(ctx, input.TenantID, func(tx *sql.Tx) error {
		current, err := lockStackIdentityRevision(ctx, tx, input.TenantID, input.OwnerSubjectID)
		if err != nil {
			return err
		}
		var storedHash, storedJSON string
		var storedRevision int64
		err = tx.QueryRowContext(ctx, `SELECT payload_sha256, revision, result_json::text
			FROM homelab_identity_mutation_receipts
			WHERE tenant_id = $1 AND owner_subject_id = $2 AND mutation_id = $3`,
			input.TenantID, input.OwnerSubjectID, input.MutationID).Scan(&storedHash, &storedRevision, &storedJSON)
		if err == nil {
			if storedHash != input.PayloadSHA256 {
				return ErrIdentityMutationConflict
			}
			var original Homelab
			if err := json.Unmarshal([]byte(storedJSON), &original); err != nil {
				return err
			}
			result = &StackIdentityMutationResult{Homelab: &original, Revision: storedRevision, Replayed: true}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if current != input.ExpectedRevision {
			return ErrIdentityRevisionConflict
		}
		homelab, err := s.insertHomelab(ctx, tx, CreateHomelabRequest{
			ID: input.HomelabID, TenantID: input.TenantID,
			OwnerSubjectID: input.OwnerSubjectID, Name: input.Write.Name,
		})
		if err != nil {
			return err
		}
		if homelab == nil {
			homelab, err = selectHomelabByOwner(ctx, tx, input.TenantID, input.OwnerSubjectID)
			if err != nil {
				return err
			}
		}
		presentation := input.Write.Presentation
		if presentation == nil {
			presentation = homelab.Identity.Presentation
		}
		var presentationJSON any
		if presentation != nil {
			raw, err := json.Marshal(presentation)
			if err != nil {
				return err
			}
			presentationJSON = string(raw)
		}
		editedAt := input.Write.EditedAt
		if editedAt.IsZero() {
			editedAt = time.Now().UTC()
		}
		updated, err := scanHomelab(tx.QueryRowContext(ctx, `UPDATE homelabs
			SET name = $4, identity_presentation = $5::jsonb, identity_pending = true,
				named_at = $6, updated_at = now()
			WHERE tenant_id = $1 AND id = $2 AND owner_subject_id = $3 AND deleted_at IS NULL
			RETURNING `+homelabColumns, input.TenantID, homelab.ID, input.OwnerSubjectID,
			input.Write.Name, presentationJSON, editedAt))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		revision, err := advanceStackIdentityRevision(ctx, tx, input.TenantID, input.OwnerSubjectID)
		if err != nil {
			return err
		}
		// Store only the identity fields needed to reconstruct the original
		// response, not the homelab's intent or other operator data.
		receiptIdentity := &Homelab{ID: updated.ID, TenantID: updated.TenantID,
			OwnerSubjectID: updated.OwnerSubjectID, Name: updated.Name,
			NamedAt: updated.NamedAt, Identity: updated.Identity}
		receiptJSON, err := json.Marshal(receiptIdentity)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO homelab_identity_mutation_receipts
			(tenant_id, owner_subject_id, mutation_id, payload_sha256, revision, result_json)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb)`, input.TenantID, input.OwnerSubjectID,
			input.MutationID, input.PayloadSHA256, revision, string(receiptJSON))
		if err != nil {
			return err
		}
		result = &StackIdentityMutationResult{Homelab: receiptIdentity, Revision: revision}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
