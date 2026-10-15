package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const workerIdentityColumns = `tenant_id, worker_id, state, key_thumbprint, public_key_jwk::text,
	attempt_id, pairing_token_hash, credential_epoch, previous_key_thumbprint,
	previous_public_key_jwk::text, previous_valid_until, issued_at, confirmed_at,
	revoked_at, updated_at, approved_by, approved_at`

func scanWorkerIdentity(row rowScanner) (*WorkerIdentity, error) {
	var identity WorkerIdentity
	var attempt, pairing, previousThumbprint, previousJWK, approvedBy sql.NullString
	var previousValidUntil, confirmedAt, revokedAt, approvedAt sql.NullTime
	if err := row.Scan(
		&identity.TenantID, &identity.WorkerID, &identity.State, &identity.Thumbprint, &identity.PublicKeyJWK,
		&attempt, &pairing, &identity.CredentialEpoch, &previousThumbprint, &previousJWK,
		&previousValidUntil, &identity.IssuedAt, &confirmedAt, &revokedAt, &identity.UpdatedAt,
		&approvedBy, &approvedAt,
	); err != nil {
		return nil, err
	}
	identity.AttemptID, identity.PairingTokenHash = attempt.String, pairing.String
	identity.PreviousThumbprint, identity.PreviousPublicKeyJWK = previousThumbprint.String, previousJWK.String
	if previousValidUntil.Valid {
		identity.PreviousValidUntil = &previousValidUntil.Time
	}
	if confirmedAt.Valid {
		identity.ConfirmedAt = &confirmedAt.Time
	}
	if revokedAt.Valid {
		identity.RevokedAt = &revokedAt.Time
	}
	identity.ApprovedBy = approvedBy.String
	if approvedAt.Valid {
		identity.ApprovedAt = &approvedAt.Time
	}
	return &identity, nil
}

func (s *PostgresStore) identityReady() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	return nil
}

func (s *PostgresStore) GetWorkerIdentity(ctx context.Context, tenantID, workerID string) (*WorkerIdentity, error) {
	if err := s.identityReady(); err != nil {
		return nil, err
	}
	tenantID, workerID, err := normalizeIdentityScope(tenantID, workerID)
	if err != nil {
		return nil, ErrNotFound
	}
	var out *WorkerIdentity
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		identity, queryErr := scanWorkerIdentity(tx.QueryRowContext(ctx,
			`SELECT `+workerIdentityColumns+` FROM worker_identities WHERE tenant_id = $1 AND worker_id = $2`,
			tenantID, workerID))
		if errors.Is(queryErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		out = identity
		return queryErr
	})
	return out, err
}

func (s *PostgresStore) GetWorkerIdentityByPairing(ctx context.Context, tenantID, pairingTokenHash string) (*WorkerIdentity, error) {
	if err := s.identityReady(); err != nil {
		return nil, err
	}
	tenantID, pairingTokenHash = strings.TrimSpace(tenantID), strings.TrimSpace(pairingTokenHash)
	if tenantID == "" || pairingTokenHash == "" {
		return nil, ErrNotFound
	}
	var out *WorkerIdentity
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		identity, queryErr := scanWorkerIdentity(tx.QueryRowContext(ctx,
			`SELECT `+workerIdentityColumns+` FROM worker_identities WHERE tenant_id = $1 AND pairing_token_hash = $2`,
			tenantID, pairingTokenHash))
		if errors.Is(queryErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		out = identity
		return queryErr
	})
	return out, err
}

func (s *PostgresStore) GetUnconfirmedWorkerIdentityByKey(ctx context.Context, tenantID, stackID, thumbprint string) (*WorkerIdentity, error) {
	if err := s.identityReady(); err != nil {
		return nil, err
	}
	tenantID, stackID, thumbprint = strings.TrimSpace(tenantID), strings.TrimSpace(stackID), strings.TrimSpace(thumbprint)
	if tenantID == "" || stackID == "" || thumbprint == "" {
		return nil, ErrNotFound
	}
	var out *WorkerIdentity
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		identity, queryErr := scanWorkerIdentity(tx.QueryRowContext(ctx, `
			SELECT `+workerIdentityColumns+` FROM worker_identities
			WHERE tenant_id = $1 AND key_thumbprint = $2 AND state = 'issued_unconfirmed'
			  AND EXISTS (
				SELECT 1 FROM workers w
				WHERE w.tenant_id = worker_identities.tenant_id AND w.id = worker_identities.worker_id
				  AND w.stack_id = $3
			  )
			ORDER BY issued_at DESC LIMIT 1`,
			tenantID, thumbprint, stackID))
		if errors.Is(queryErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		out = identity
		return queryErr
	})
	return out, err
}

func (s *PostgresStore) IssueWorkerIdentity(ctx context.Context, command WorkerIdentityIssue) (*WorkerIdentity, error) {
	if err := s.identityReady(); err != nil {
		return nil, err
	}
	tenantID, workerID, err := normalizeIdentityScope(command.TenantID, command.WorkerID)
	if err != nil {
		return nil, err
	}
	if err := validateIdentityKey(command.Thumbprint, command.PublicKeyJWK); err != nil {
		return nil, err
	}
	var out *WorkerIdentity
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		if credential := strings.TrimSpace(command.CredentialSHA256); credential != "" {
			result, execErr := tx.ExecContext(ctx, `
				UPDATE workers
				SET resources_json = COALESCE(resources_json, '{}'::jsonb) ||
						jsonb_build_object('agent_token_sha256', $3::text),
					updated_at = now()
				WHERE tenant_id = $1 AND id = $2
			`, tenantID, workerID, credential)
			if execErr != nil {
				return execErr
			}
			if affected, _ := result.RowsAffected(); affected != 1 {
				return ErrNotFound
			}
		}
		identity, queryErr := scanWorkerIdentity(tx.QueryRowContext(ctx, `
			INSERT INTO worker_identities (
				tenant_id, worker_id, state, key_thumbprint, public_key_jwk, attempt_id,
				pairing_token_hash, credential_epoch, issued_at
			) VALUES ($1, $2, 'issued_unconfirmed', $3, $4::jsonb, NULLIF($5, ''), NULLIF($6, ''), 1, $7)
			ON CONFLICT (tenant_id, worker_id) DO UPDATE SET
				state = 'issued_unconfirmed',
				key_thumbprint = EXCLUDED.key_thumbprint,
				public_key_jwk = EXCLUDED.public_key_jwk,
				attempt_id = EXCLUDED.attempt_id,
				pairing_token_hash = EXCLUDED.pairing_token_hash,
				credential_epoch = worker_identities.credential_epoch + 1,
				previous_key_thumbprint = NULL,
				previous_public_key_jwk = NULL,
				previous_valid_until = NULL,
				issued_at = EXCLUDED.issued_at,
				confirmed_at = NULL,
				revoked_at = NULL,
				approved_by = NULL,
				approved_at = NULL
			RETURNING `+workerIdentityColumns,
			tenantID, workerID, command.Thumbprint, command.PublicKeyJWK, command.AttemptID,
			command.PairingTokenHash, command.IssuedAt.UTC()))
		if queryErr != nil {
			return queryErr
		}
		out = identity
		return wakeGuardReachTx(ctx, tx, tenantID, workerID)
	})
	return out, err
}

func (s *PostgresStore) ReissueUnconfirmedWorkerIdentity(ctx context.Context, command WorkerIdentityReissue) (*WorkerIdentity, error) {
	if err := s.identityReady(); err != nil {
		return nil, err
	}
	tenantID, workerID, err := normalizeIdentityScope(command.TenantID, command.WorkerID)
	if err != nil {
		return nil, err
	}
	var out *WorkerIdentity
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		// The identity row lock serializes concurrent repetitions: each
		// replacement revokes its predecessor, so only the credential stored
		// last can ever authenticate, never two.
		current, queryErr := scanWorkerIdentity(tx.QueryRowContext(ctx,
			`SELECT `+workerIdentityColumns+` FROM worker_identities WHERE tenant_id = $1 AND worker_id = $2 FOR UPDATE`,
			tenantID, workerID))
		if errors.Is(queryErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		if queryErr != nil {
			return queryErr
		}
		if current.State != WorkerIdentityIssuedUnconfirmed ||
			current.PairingTokenHash == "" || current.PairingTokenHash != command.PairingTokenHash ||
			current.AttemptID == "" || current.AttemptID != command.AttemptID ||
			current.Thumbprint != command.Thumbprint {
			return ErrConflict
		}
		if !command.Now.Before(command.Deadline) {
			return ErrWorkerIdentityWindowClosed
		}
		result, execErr := tx.ExecContext(ctx, `
			UPDATE workers
			SET resources_json = COALESCE(resources_json, '{}'::jsonb) ||
					jsonb_build_object('agent_token_sha256', $3::text),
				updated_at = now()
			WHERE tenant_id = $1 AND id = $2
		`, tenantID, workerID, command.NewCredentialSHA256)
		if execErr != nil {
			return execErr
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return ErrNotFound
		}
		updated, updateErr := scanWorkerIdentity(tx.QueryRowContext(ctx, `
			UPDATE worker_identities SET credential_epoch = credential_epoch + 1
			WHERE tenant_id = $1 AND worker_id = $2
			RETURNING `+workerIdentityColumns, tenantID, workerID))
		if updateErr != nil {
			return updateErr
		}
		out = updated
		return nil
	})
	return out, err
}

func (s *PostgresStore) BindWorkerIdentity(ctx context.Context, command WorkerIdentityBind) (*WorkerIdentity, error) {
	if err := s.identityReady(); err != nil {
		return nil, err
	}
	tenantID, workerID, err := normalizeIdentityScope(command.TenantID, command.WorkerID)
	if err != nil {
		return nil, err
	}
	if err := validateIdentityKey(command.Thumbprint, command.PublicKeyJWK); err != nil {
		return nil, err
	}
	expected := strings.TrimSpace(command.ExpectedCredentialSHA256)
	var out *WorkerIdentity
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		// Holding the worker row keeps a concurrent credential rotation from
		// interleaving with the bind: the bind only succeeds for the exact
		// credential that authenticated it.
		worker, workerErr := scanWorker(tx.QueryRowContext(ctx, `
			SELECT id, tenant_id, instance_id, stack_id, hostname, ip, os, arch, token_hash,
				status, approved, approved_at, last_seen_at, cpu_cores, ram_mb, disk_gb,
				gpu, has_nvme, has_hw_transcode, docker_version, type, provider,
				tags_json::text, owner_subject_id, capabilities_json::text, resources_json::text,
				created_at, updated_at
			FROM workers WHERE tenant_id = $1 AND id = $2 FOR UPDATE
		`, tenantID, workerID))
		if errors.Is(workerErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		if workerErr != nil {
			return workerErr
		}
		if workerCredentialHash(*worker) != expected {
			return ErrConflict
		}
		existing, existingErr := scanWorkerIdentity(tx.QueryRowContext(ctx,
			`SELECT `+workerIdentityColumns+` FROM worker_identities WHERE tenant_id = $1 AND worker_id = $2 FOR UPDATE`,
			tenantID, workerID))
		now := command.Now.UTC()
		switch {
		case existingErr == nil:
			expired := !now.Before(existing.IssuedAt.Add(WorkerIdentityUpgradeWindow))
			switch {
			case existing.State == WorkerIdentityRevoked:
				return ErrWorkerIdentityRevoked
			case existing.Thumbprint == command.Thumbprint:
				if existing.State == WorkerIdentityUpgradePending && expired {
					refreshed, refreshErr := scanWorkerIdentity(tx.QueryRowContext(ctx, `
						UPDATE worker_identities SET issued_at = $3
						WHERE tenant_id = $1 AND worker_id = $2
						RETURNING `+workerIdentityColumns, tenantID, workerID, now))
					if refreshErr != nil {
						return refreshErr
					}
					existing = refreshed
				}
				out = existing
				return nil
			case existing.State == WorkerIdentityUpgradePending && !expired:
				return ErrWorkerIdentityUpgradePending
			case existing.State != WorkerIdentityUpgradePending:
				return ErrWorkerIdentityBound
			}
		case !errors.Is(existingErr, sql.ErrNoRows):
			return existingErr
		}
		// A legacy worker's key is held as pending: the bearer credential that
		// authenticated this request proves nothing about which key belongs to
		// the server, so only the owner's approval of this exact key binds it.
		pending, upsertErr := scanWorkerIdentity(tx.QueryRowContext(ctx, `
			INSERT INTO worker_identities (
				tenant_id, worker_id, state, key_thumbprint, public_key_jwk, attempt_id,
				credential_epoch, issued_at
			) VALUES ($1, $2, 'upgrade_pending', $3, $4::jsonb, NULLIF($5, ''), 1, $6)
			ON CONFLICT (tenant_id, worker_id) DO UPDATE SET
				state = 'upgrade_pending',
				key_thumbprint = EXCLUDED.key_thumbprint,
				public_key_jwk = EXCLUDED.public_key_jwk,
				attempt_id = EXCLUDED.attempt_id,
				issued_at = EXCLUDED.issued_at,
				confirmed_at = NULL, approved_by = NULL, approved_at = NULL
			RETURNING `+workerIdentityColumns,
			tenantID, workerID, command.Thumbprint, command.PublicKeyJWK, command.AttemptID, now))
		if upsertErr != nil {
			return upsertErr
		}
		out = pending
		return nil
	})
	return out, err
}

func (s *PostgresStore) ApproveWorkerIdentityUpgrade(ctx context.Context, command WorkerIdentityUpgradeDecision) (*WorkerIdentity, error) {
	if err := s.identityReady(); err != nil {
		return nil, err
	}
	tenantID, workerID, err := normalizeIdentityScope(command.TenantID, command.WorkerID)
	if err != nil {
		return nil, err
	}
	var out *WorkerIdentity
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		now := command.Now.UTC()
		approved, queryErr := scanWorkerIdentity(tx.QueryRowContext(ctx, `
			UPDATE worker_identities
			SET state = 'bound', confirmed_at = $4, approved_by = $5, approved_at = $4
			WHERE tenant_id = $1 AND worker_id = $2 AND state = 'upgrade_pending'
			  AND key_thumbprint = $3 AND issued_at > $6
			RETURNING `+workerIdentityColumns,
			tenantID, workerID, strings.TrimSpace(command.Thumbprint), now, strings.TrimSpace(command.ActorID),
			now.Add(-WorkerIdentityUpgradeWindow)))
		if errors.Is(queryErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		if queryErr != nil {
			return queryErr
		}
		out = approved
		return wakeGuardReachTx(ctx, tx, tenantID, workerID)
	})
	return out, err
}

func (s *PostgresStore) RejectWorkerIdentityUpgrade(ctx context.Context, tenantID, workerID string) error {
	if err := s.identityReady(); err != nil {
		return err
	}
	tenantID, workerID, err := normalizeIdentityScope(tenantID, workerID)
	if err != nil {
		return err
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx, `
			DELETE FROM worker_identities WHERE tenant_id = $1 AND worker_id = $2 AND state = 'upgrade_pending'
		`, tenantID, workerID)
		return execErr
	})
}

func (s *PostgresStore) ListWorkerIdentityUpgrades(ctx context.Context, tenantID string) ([]WorkerIdentity, error) {
	if err := s.identityReady(); err != nil {
		return nil, err
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, nil
	}
	var out []WorkerIdentity
	err := s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		rows, queryErr := tx.QueryContext(ctx,
			`SELECT `+workerIdentityColumns+` FROM worker_identities WHERE tenant_id = $1 AND state = 'upgrade_pending' ORDER BY worker_id LIMIT 500`,
			tenantID)
		if queryErr != nil {
			return queryErr
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			identity, scanErr := scanWorkerIdentity(rows)
			if scanErr != nil {
				return scanErr
			}
			out = append(out, *identity)
		}
		return rows.Err()
	})
	return out, err
}

func (s *PostgresStore) ConfirmWorkerIdentity(ctx context.Context, tenantID, workerID, thumbprint string, now time.Time) (*WorkerIdentity, error) {
	if err := s.identityReady(); err != nil {
		return nil, err
	}
	tenantID, workerID, err := normalizeIdentityScope(tenantID, workerID)
	if err != nil {
		return nil, err
	}
	var out *WorkerIdentity
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		current, queryErr := scanWorkerIdentity(tx.QueryRowContext(ctx,
			`SELECT `+workerIdentityColumns+` FROM worker_identities WHERE tenant_id = $1 AND worker_id = $2 FOR UPDATE`,
			tenantID, workerID))
		if errors.Is(queryErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		if queryErr != nil {
			return queryErr
		}
		if current.State == WorkerIdentityRevoked {
			return ErrWorkerIdentityRevoked
		}
		if !current.AcceptsThumbprint(thumbprint, now) {
			return ErrConflict
		}
		if current.State == WorkerIdentityIssuedUnconfirmed {
			confirmed, updateErr := scanWorkerIdentity(tx.QueryRowContext(ctx, `
				UPDATE worker_identities SET state = 'bound', confirmed_at = $3
				WHERE tenant_id = $1 AND worker_id = $2
				RETURNING `+workerIdentityColumns, tenantID, workerID, now.UTC()))
			if updateErr != nil {
				return updateErr
			}
			current = confirmed
			if wakeErr := wakeGuardReachTx(ctx, tx, tenantID, workerID); wakeErr != nil {
				return wakeErr
			}
		}
		out = current
		return nil
	})
	return out, err
}

func (s *PostgresStore) RotateWorkerIdentity(ctx context.Context, command WorkerIdentityRotation) (*WorkerIdentity, error) {
	if err := s.identityReady(); err != nil {
		return nil, err
	}
	tenantID, workerID, err := normalizeIdentityScope(command.TenantID, command.WorkerID)
	if err != nil {
		return nil, err
	}
	if err := validateIdentityKey(command.NewThumbprint, command.NewPublicJWK); err != nil {
		return nil, err
	}
	var out *WorkerIdentity
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		current, queryErr := scanWorkerIdentity(tx.QueryRowContext(ctx,
			`SELECT `+workerIdentityColumns+` FROM worker_identities WHERE tenant_id = $1 AND worker_id = $2 FOR UPDATE`,
			tenantID, workerID))
		if errors.Is(queryErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		if queryErr != nil {
			return queryErr
		}
		switch {
		case current.State == WorkerIdentityRevoked:
			return ErrWorkerIdentityRevoked
		case current.Thumbprint == command.NewThumbprint:
			out = current
			return nil
		case current.State != WorkerIdentityBound || current.Thumbprint != command.OldThumbprint:
			return ErrConflict
		}
		rotated, updateErr := scanWorkerIdentity(tx.QueryRowContext(ctx, `
			UPDATE worker_identities SET
				previous_key_thumbprint = key_thumbprint,
				previous_public_key_jwk = public_key_jwk,
				previous_valid_until = $6,
				key_thumbprint = $4,
				public_key_jwk = $5::jsonb
			WHERE tenant_id = $1 AND worker_id = $2 AND key_thumbprint = $3
			RETURNING `+workerIdentityColumns,
			tenantID, workerID, command.OldThumbprint, command.NewThumbprint, command.NewPublicJWK,
			command.Now.UTC().Add(command.Overlap)))
		if updateErr != nil {
			return updateErr
		}
		out = rotated
		return wakeGuardReachTx(ctx, tx, tenantID, workerID)
	})
	return out, err
}

func (s *PostgresStore) RevokeWorkerIdentity(ctx context.Context, tenantID, workerID string, now time.Time) error {
	if err := s.identityReady(); err != nil {
		return err
	}
	tenantID, workerID, err := normalizeIdentityScope(tenantID, workerID)
	if err != nil {
		return err
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		return revokeWorkerIdentityTx(ctx, tx, tenantID, workerID, now)
	})
}

// revokeWorkerIdentityTx ends a host-key binding inside a caller transaction
// so detach and retire can revoke credential and key together.
func revokeWorkerIdentityTx(ctx context.Context, tx *sql.Tx, tenantID, workerID string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE worker_identities SET state = 'revoked', revoked_at = COALESCE(revoked_at, $3)
		WHERE tenant_id = $1 AND worker_id = $2 AND state <> 'revoked'
	`, tenantID, workerID, now.UTC()); err != nil {
		return err
	}
	// The same transaction obliges the reach reconciler to revoke the server at
	// kombify.me and Connect, so detach never leaves a reachable server behind.
	_, err := tx.ExecContext(ctx, `
		UPDATE guard_reach_states
		SET desired_state = 'revoked', next_attempt_at = now(), attempts = 0
		WHERE tenant_id = $1 AND worker_id = $2 AND desired_state <> 'revoked'
	`, tenantID, workerID)
	return err
}

// wakeGuardReachTx makes the Guard reach reconciler look at a worker now: a
// new pairing, a confirmed key or a rotated key changes what must be
// registered. A worker without a reach row (self-managed editions) is a no-op.
func wakeGuardReachTx(ctx context.Context, tx *sql.Tx, tenantID, workerID string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE guard_reach_states SET next_attempt_at = now()
		WHERE tenant_id = $1 AND worker_id = $2
	`, tenantID, workerID)
	return err
}

func (s *PostgresStore) ConsumeWorkerProof(ctx context.Context, tenantID, workerID, jti string, replayUntil time.Time) error {
	if err := s.identityReady(); err != nil {
		return err
	}
	tenantID, workerID, err := normalizeIdentityScope(tenantID, workerID)
	if err != nil {
		return err
	}
	return s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		// Expired identifiers of this worker are pruned with the same
		// statement that records the new one, so the ledger stays bounded
		// without a separate sweeper. expires_at is computed on the verifying
		// replica's clock and the sweep runs on the database clock: an identifier
		// is only dropped WorkerProofPruneSkew after it expired, so a replica
		// whose clock runs behind the database cannot get a still-acceptable
		// proof pruned and replayed.
		result, execErr := tx.ExecContext(ctx, `
			WITH pruned AS (
				DELETE FROM worker_proof_jtis
				WHERE tenant_id = $1 AND worker_id = $2 AND expires_at < clock_timestamp() - make_interval(secs => $5)
			)
			INSERT INTO worker_proof_jtis (tenant_id, worker_id, jti, expires_at)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (tenant_id, worker_id, jti) DO NOTHING
		`, tenantID, workerID, jti, replayUntil.UTC(), WorkerProofPruneSkew.Seconds())
		if execErr != nil {
			return execErr
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return ErrWorkerProofReplayed
		}
		return nil
	})
}

var _ WorkerIdentityStore = (*PostgresStore)(nil)
