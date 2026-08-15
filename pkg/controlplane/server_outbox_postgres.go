package controlplane

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/serverregistry"
)

var _ serverregistry.OutboxWorkRepository = (*PostgresStore)(nil)

// ClaimServerRegistryOutbox claims one due item inside its tenant RLS scope.
func (s *PostgresStore) ClaimOutbox(ctx context.Context, tenantID, owner string, lease time.Duration) (*serverregistry.OutboxClaim, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("controlplane: database not configured")
	}
	tenantID, owner = strings.TrimSpace(tenantID), strings.TrimSpace(owner)
	if tenantID == "" || owner == "" || lease < time.Second {
		return nil, fmt.Errorf("controlplane: tenant, claim owner, and a lease of at least one second are required")
	}
	token, err := newServerOutboxClaimToken()
	if err != nil {
		return nil, err
	}
	var claim *serverregistry.OutboxClaim
	err = s.withTenant(ctx, tenantID, func(tx *sql.Tx) error {
		stored := &serverregistry.OutboxClaim{}
		var payloadJSON string
		queryErr := tx.QueryRowContext(ctx, `
			WITH candidate AS (
				SELECT id
				FROM server_registry_outbox
				WHERE tenant_id = $1
				  AND published_at IS NULL
				  AND available_at <= clock_timestamp()
				  AND (claim_expires_at IS NULL OR claim_expires_at <= clock_timestamp())
				ORDER BY available_at, id
				LIMIT 1
				FOR UPDATE SKIP LOCKED
			), claimed AS (
				UPDATE server_registry_outbox AS work
				SET claim_generation = work.claim_generation + 1,
					claim_owner = $2,
					claim_token_digest = $3,
					claim_expires_at = clock_timestamp() + ($4 * interval '1 second'),
					attempts = work.attempts + 1
				FROM candidate
				WHERE work.id = candidate.id AND work.tenant_id = $1
				RETURNING work.id, work.tenant_id, work.server_id,
					work.aggregate_revision, work.generation, work.authority,
					work.source, work.event_type, work.payload_json::text,
					work.occurred_at, work.created_at, work.published_at,
					work.attempts, work.claim_generation, work.claim_owner,
					work.claim_expires_at
			)
			SELECT * FROM claimed
		`, tenantID, owner, serverOutboxClaimDigest(token), int64(lease/time.Second)).Scan(
			&stored.ID, &stored.TenantID, &stored.ServerID,
			&stored.AggregateRevision, &stored.Generation, &stored.Authority,
			&stored.Source, &stored.EventType, &payloadJSON,
			&stored.OccurredAt, &stored.CreatedAt, &stored.PublishedAt,
			&stored.Attempts, &stored.ClaimGeneration, &stored.ClaimOwner,
			&stored.ClaimExpiresAt,
		)
		if errors.Is(queryErr, sql.ErrNoRows) {
			return serverregistry.ErrOutboxEmpty
		}
		if queryErr != nil {
			return queryErr
		}
		if unmarshalErr := json.Unmarshal([]byte(payloadJSON), &stored.Payload); unmarshalErr != nil {
			return unmarshalErr
		}
		stored.ClaimToken = token
		claim = stored
		return nil
	})
	return claim, err
}

// HeartbeatOutbox extends only a live matching claim using database time.
func (s *PostgresStore) HeartbeatOutbox(ctx context.Context, claim serverregistry.OutboxClaim, lease time.Duration) error {
	if lease < time.Second {
		return serverregistry.ErrOutboxClaimLost
	}
	return s.mutateServerOutboxClaim(ctx, claim, func(ctx context.Context, tx *sql.Tx) (sql.Result, error) {
		return tx.ExecContext(ctx, `
			UPDATE server_registry_outbox
			SET claim_expires_at = clock_timestamp() + ($9 * interval '1 second')
			WHERE tenant_id = $1 AND id = $2 AND server_id = $3
			  AND aggregate_revision = $4 AND generation = $5
			  AND claim_generation = $6 AND claim_owner = $7
			  AND claim_token_digest = $8 AND claim_expires_at > clock_timestamp()
			  AND published_at IS NULL
		`, claim.TenantID, claim.ID, claim.ServerID, claim.AggregateRevision,
			claim.Generation, claim.ClaimGeneration, claim.ClaimOwner,
			serverOutboxClaimDigest(claim.ClaimToken), int64(lease/time.Second))
	})
}

// CompleteOutbox publishes only the exact live claim.
func (s *PostgresStore) CompleteOutbox(ctx context.Context, claim serverregistry.OutboxClaim) error {
	return s.mutateServerOutboxClaim(ctx, claim, func(ctx context.Context, tx *sql.Tx) (sql.Result, error) {
		return tx.ExecContext(ctx, `
			UPDATE server_registry_outbox
			SET published_at = clock_timestamp(), claim_owner = NULL,
				claim_token_digest = NULL, claim_expires_at = NULL, last_error = NULL
			WHERE tenant_id = $1 AND id = $2 AND server_id = $3
			  AND aggregate_revision = $4 AND generation = $5
			  AND claim_generation = $6 AND claim_owner = $7
			  AND claim_token_digest = $8 AND claim_expires_at > clock_timestamp()
			  AND published_at IS NULL
		`, claim.TenantID, claim.ID, claim.ServerID, claim.AggregateRevision,
			claim.Generation, claim.ClaimGeneration, claim.ClaimOwner,
			serverOutboxClaimDigest(claim.ClaimToken))
	})
}

// RetryOutbox releases a live claim and schedules it using database time.
func (s *PostgresStore) RetryOutbox(ctx context.Context, claim serverregistry.OutboxClaim, delay time.Duration, cause error) error {
	if delay < 0 {
		return serverregistry.ErrOutboxClaimLost
	}
	lastError := ""
	if cause != nil {
		lastError = cause.Error()
	}
	return s.mutateServerOutboxClaim(ctx, claim, func(ctx context.Context, tx *sql.Tx) (sql.Result, error) {
		return tx.ExecContext(ctx, `
			UPDATE server_registry_outbox
			SET available_at = clock_timestamp() + ($9 * interval '1 second'),
				claim_owner = NULL, claim_token_digest = NULL,
				claim_expires_at = NULL, last_error = NULLIF($10, '')
			WHERE tenant_id = $1 AND id = $2 AND server_id = $3
			  AND aggregate_revision = $4 AND generation = $5
			  AND claim_generation = $6 AND claim_owner = $7
			  AND claim_token_digest = $8 AND claim_expires_at > clock_timestamp()
			  AND published_at IS NULL
		`, claim.TenantID, claim.ID, claim.ServerID, claim.AggregateRevision,
			claim.Generation, claim.ClaimGeneration, claim.ClaimOwner,
			serverOutboxClaimDigest(claim.ClaimToken), int64(delay/time.Second), lastError)
	})
}

func (s *PostgresStore) mutateServerOutboxClaim(
	ctx context.Context,
	claim serverregistry.OutboxClaim,
	mutate func(context.Context, *sql.Tx) (sql.Result, error),
) error {
	if s == nil || s.db == nil || strings.TrimSpace(claim.TenantID) == "" || claim.ID <= 0 ||
		strings.TrimSpace(claim.ServerID) == "" || claim.AggregateRevision <= 0 || claim.Generation <= 0 ||
		claim.ClaimGeneration <= 0 || strings.TrimSpace(claim.ClaimOwner) == "" || strings.TrimSpace(claim.ClaimToken) == "" {
		return serverregistry.ErrOutboxClaimLost
	}
	return s.withTenant(ctx, claim.TenantID, func(tx *sql.Tx) error {
		result, err := mutate(ctx, tx)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows != 1 {
			return serverregistry.ErrOutboxClaimLost
		}
		return nil
	})
}

func newServerOutboxClaimToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("controlplane: generate server outbox claim token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func serverOutboxClaimDigest(token string) []byte {
	digest := sha256.Sum256([]byte(token))
	return digest[:]
}
