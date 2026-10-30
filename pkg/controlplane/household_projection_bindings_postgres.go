package controlplane

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"strings"

	"github.com/kombifyio/techstack/internal/gocommon/tenantdb"
)

const householdProjectionColumns = `tenant_id, owner_subject_id, cloud_subject, revision, action, effect_digest,
	username, stack_id, agent_id, stackkit_instance_id`

func scanHouseholdProjection(row rowScanner) (*HouseholdProjectionBinding, error) {
	var b HouseholdProjectionBinding
	err := row.Scan(&b.TenantID, &b.OwnerSubjectID, &b.CloudSubject, &b.Revision, &b.Action, &b.EffectDigest,
		&b.Username, &b.StackID, &b.AgentID, &b.StackKitInstanceID)
	return &b, err
}

// User work requires the canonical owner context. Durable projection work uses
// only the separate verified SQL login; app GUCs never grant system authority.
func (s *PostgresStore) withHouseholdProjection(ctx context.Context, intent HouseholdProjectionIntent, fn func(*sql.Tx) error) error {
	if s == nil || s.db == nil || ctx == nil ||
		intent.TenantID == "" || intent.OwnerSubjectID == "" || intent.CloudSubject == "" || intent.OwnerSubjectID == intent.CloudSubject {
		return ErrHouseholdProjectionUnavailable
	}
	return s.withTenant(ctx, intent.TenantID, func(tx *sql.Tx) error {
		if tenantdb.ContextTokenFromContext(ctx) == "" {
			if err := verifyHouseholdSystemScope(ctx, tx, intent); err != nil {
				return err
			}
		} else {
			var subject, tenant sql.NullString
			if err := tx.QueryRowContext(ctx, `SELECT kombify_ctx.subject(), kombify_ctx.org_id()`).Scan(&subject, &tenant); err != nil {
				return err
			}
			if !subject.Valid || !tenant.Valid || subject.String != intent.OwnerSubjectID || tenant.String != intent.TenantID {
				return ErrHouseholdProjectionUnavailable
			}
		}
		key := sha256.Sum256([]byte(intent.TenantID + "\x00" + intent.OwnerSubjectID + "\x00" + intent.CloudSubject))
		// Hash before sending PostgreSQL text (the composite contains NULs).
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, hex.EncodeToString(key[:])); err != nil {
			return err
		}
		return fn(tx)
	})
}

func loadHouseholdProjection(ctx context.Context, tx *sql.Tx, intent HouseholdProjectionIntent) (*HouseholdProjectionBinding, error) {
	return scanHouseholdProjection(tx.QueryRowContext(ctx, `SELECT `+householdProjectionColumns+`
		FROM household_projection_bindings WHERE tenant_id=$1 AND owner_subject_id=$2 AND cloud_subject=$3 FOR UPDATE`,
		intent.TenantID, intent.OwnerSubjectID, intent.CloudSubject))
}

func checkHouseholdProjection(current *HouseholdProjectionBinding, expected HouseholdProjectionIntent) error {
	if current.Revision > expected.Revision {
		return ErrHouseholdProjectionStale
	}
	if current.HouseholdProjectionIntent != expected {
		return ErrHouseholdProjectionConflict
	}
	return nil
}

func (s *PostgresStore) AcceptHouseholdProjection(ctx context.Context, intent HouseholdProjectionIntent) (*HouseholdProjectionBinding, error) {
	digest, err := hex.DecodeString(intent.EffectDigest)
	if err != nil || len(digest) != sha256.Size || intent.EffectDigest != strings.ToLower(intent.EffectDigest) ||
		intent.Revision == 0 || intent.Revision > math.MaxInt64 || (intent.Action != "add" && intent.Action != "remove") {
		return nil, ErrHouseholdProjectionConflict
	}
	var out *HouseholdProjectionBinding
	err = s.withHouseholdProjection(ctx, intent, func(tx *sql.Tx) error {
		current, err := loadHouseholdProjection(ctx, tx, intent)
		if errors.Is(err, sql.ErrNoRows) {
			// Adopt a legacy reference exactly once, under the same pair lock.
			// Several legacy targets need explicit reconciliation, not a guess.
			rows, err := tx.QueryContext(ctx, `SELECT stack_id, username FROM household_identity_refs
				WHERE tenant_id=$1 AND owner_subject_id=$2 AND cloud_subject=$3 ORDER BY stack_id FOR UPDATE`,
				intent.TenantID, intent.OwnerSubjectID, intent.CloudSubject)
			if err != nil {
				return err
			}
			person := sha256.Sum256([]byte(intent.CloudSubject))
			current = &HouseholdProjectionBinding{HouseholdProjectionIntent: intent, Username: "member-" + hex.EncodeToString(person[:])[:20]}
			count := 0
			for rows.Next() {
				count++
				if err := rows.Scan(&current.StackID, &current.Username); err != nil {
					rows.Close()
					return err
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if count > 1 {
				return ErrHouseholdProjectionConflict
			}
			out, err = scanHouseholdProjection(tx.QueryRowContext(ctx, `INSERT INTO household_projection_bindings
				(tenant_id, owner_subject_id, cloud_subject, revision, action, effect_digest, username, stack_id)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+householdProjectionColumns,
				intent.TenantID, intent.OwnerSubjectID, intent.CloudSubject, intent.Revision, intent.Action, intent.EffectDigest, current.Username, current.StackID))
			return err
		}
		if err != nil {
			return err
		}
		if intent.Revision <= current.Revision {
			if err := checkHouseholdProjection(current, intent); err != nil {
				return err
			}
			out = current
			return nil
		}
		out, err = scanHouseholdProjection(tx.QueryRowContext(ctx, `UPDATE household_projection_bindings
			SET revision=$4, action=$5, effect_digest=$6, updated_at=now()
			WHERE tenant_id=$1 AND owner_subject_id=$2 AND cloud_subject=$3 RETURNING `+householdProjectionColumns,
			intent.TenantID, intent.OwnerSubjectID, intent.CloudSubject, intent.Revision, intent.Action, intent.EffectDigest))
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) BindHouseholdProjectionTarget(ctx context.Context, expected HouseholdProjectionBinding, stackID, agentID, instanceID string) (*HouseholdProjectionBinding, error) {
	if strings.TrimSpace(stackID) == "" || strings.TrimSpace(agentID) == "" || strings.TrimSpace(instanceID) == "" {
		return nil, ErrHouseholdProjectionConflict
	}
	var out *HouseholdProjectionBinding
	err := s.withHouseholdProjection(ctx, expected.HouseholdProjectionIntent, func(tx *sql.Tx) error {
		current, err := loadHouseholdProjection(ctx, tx, expected.HouseholdProjectionIntent)
		if err != nil {
			return err
		}
		if err := checkHouseholdProjection(current, expected.HouseholdProjectionIntent); err != nil {
			return err
		}
		if current.AgentID != "" {
			// Another identical request may have won target selection. Always
			// return that committed target, never dispatch the losing proposal.
			out = current
			return nil
		}
		if current.Username != expected.Username || (current.StackID != "" && current.StackID != stackID) ||
			(current.StackID == "" && current.Action != "add") {
			return ErrHouseholdProjectionConflict
		}
		out, err = scanHouseholdProjection(tx.QueryRowContext(ctx, `UPDATE household_projection_bindings
			SET stack_id=$4, agent_id=$5, stackkit_instance_id=$6, updated_at=now()
			WHERE tenant_id=$1 AND owner_subject_id=$2 AND cloud_subject=$3 RETURNING `+householdProjectionColumns,
			current.TenantID, current.OwnerSubjectID, current.CloudSubject, stackID, agentID, instanceID))
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) CompleteHouseholdProjection(ctx context.Context, expected HouseholdProjectionBinding, email, displayName string) (*HomelabIdentityRef, error) {
	var out *HomelabIdentityRef
	err := s.withHouseholdProjection(ctx, expected.HouseholdProjectionIntent, func(tx *sql.Tx) error {
		current, err := loadHouseholdProjection(ctx, tx, expected.HouseholdProjectionIntent)
		if err != nil {
			return err
		}
		if err := checkHouseholdProjection(current, expected.HouseholdProjectionIntent); err != nil {
			return err
		}
		if *current != expected || current.StackID == "" || current.AgentID == "" {
			return ErrHouseholdProjectionConflict
		}
		if current.Action == "remove" {
			_, err = tx.ExecContext(ctx, `DELETE FROM household_identity_refs
				WHERE tenant_id=$1 AND stack_id=$2 AND cloud_subject=$3 AND owner_subject_id=$4`,
				current.TenantID, current.StackID, current.CloudSubject, current.OwnerSubjectID)
			return err
		}
		ref, err := normalizeHouseholdRef(HomelabIdentityRef{TenantID: current.TenantID, StackID: current.StackID,
			Username: current.Username, OwnerSubjectID: current.OwnerSubjectID, CloudSubject: current.CloudSubject,
			Email: email, DisplayName: displayName})
		if err != nil {
			return err
		}
		// This remains the sole link authority. Existing verified links stay
		// linked; a new or recreated reference is only invited.
		out, err = scanHouseholdRef(tx.QueryRowContext(ctx, `INSERT INTO household_identity_refs
			(tenant_id, stack_id, idp, username, role, owner_subject_id, cloud_subject, email, display_name, status)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'invited')
			ON CONFLICT (tenant_id, stack_id, cloud_subject) DO UPDATE SET
				email=EXCLUDED.email, display_name=EXCLUDED.display_name
			WHERE household_identity_refs.owner_subject_id=EXCLUDED.owner_subject_id
				AND household_identity_refs.username=EXCLUDED.username
			RETURNING `+householdRefColumns,
			ref.TenantID, ref.StackID, ref.IdP, ref.Username, ref.Role, ref.OwnerSubjectID, ref.CloudSubject, ref.Email, ref.DisplayName))
		if errors.Is(err, sql.ErrNoRows) || isUniqueViolation(err) {
			return ErrHouseholdProjectionConflict
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

var _ HouseholdProjectionStore = (*PostgresStore)(nil)
