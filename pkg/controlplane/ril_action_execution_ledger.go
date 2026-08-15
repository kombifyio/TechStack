package controlplane

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
	"github.com/google/uuid"
)

// Reserve atomically implements the shared RIL action execution ledger SPI.
func (s *PostgresStore) Reserve(ctx context.Context, request rilaction.LedgerReservationRequest) (rilaction.LedgerReservation, error) {
	if s == nil || s.db == nil {
		return rilaction.LedgerReservation{}, fmt.Errorf("controlplane: database not configured")
	}
	if strings.TrimSpace(request.TenantID) == "" || strings.TrimSpace(request.IdempotencyKey) == "" ||
		strings.TrimSpace(request.ExecutionID) == "" || strings.TrimSpace(request.RequestDigest) == "" ||
		strings.TrimSpace(request.AdmissionDigest) == "" || strings.TrimSpace(request.AuditCorrelationID) == "" {
		return rilaction.LedgerReservation{}, fmt.Errorf("controlplane: complete RIL action reservation identity required")
	}
	requestedAt, err := parseCanonicalLedgerTime(request.RequestedAt)
	if err != nil {
		return rilaction.LedgerReservation{}, fmt.Errorf("controlplane: canonical UTC requested_at required")
	}
	validUntil, err := parseCanonicalLedgerTime(request.ValidUntil)
	if err != nil || !requestedAt.Before(validUntil) {
		return rilaction.LedgerReservation{}, fmt.Errorf("controlplane: valid RIL action reservation window required")
	}
	token := "reservation-" + uuid.NewString()
	var result rilaction.LedgerReservation
	err = s.withTenant(ctx, request.TenantID, func(tx *sql.Tx) error {
		var insertedToken string
		err := tx.QueryRowContext(ctx, `
			INSERT INTO ril_action_execution_ledger (
				tenant_id, idempotency_key, execution_id, request_digest,
				execution_admission_digest, audit_correlation_id,
				requested_at, valid_until, reservation_token
			)
			SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9
			WHERE $8 > clock_timestamp()
			ON CONFLICT (tenant_id, idempotency_key) DO NOTHING
			RETURNING reservation_token
		`, request.TenantID, request.IdempotencyKey, request.ExecutionID,
			request.RequestDigest, request.AdmissionDigest, request.AuditCorrelationID,
			requestedAt, validUntil, token).Scan(&insertedToken)
		if err == nil {
			if err := appendRILExecutionLeaseAudit(ctx, tx, request, insertedToken, "acquired", 0); err != nil {
				return err
			}
			result = rilaction.LedgerReservation{Disposition: rilaction.LedgerAcquired, ReservationToken: insertedToken}
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}

		var executionID, requestDigest, admissionDigest, status, evidenceJSON, persistedCorrelation string
		var persistedValidUntil time.Time
		var takeoverCount int64
		scanErr := tx.QueryRowContext(ctx, `
			SELECT execution_id, request_digest, COALESCE(execution_admission_digest, ''),
			       status, COALESCE(evidence_json::text, ''), valid_until,
			       COALESCE(audit_correlation_id, ''), takeover_count
			FROM ril_action_execution_ledger
			WHERE tenant_id = $1 AND idempotency_key = $2
			FOR UPDATE
		`, request.TenantID, request.IdempotencyKey).Scan(
			&executionID, &requestDigest, &admissionDigest, &status, &evidenceJSON,
			&persistedValidUntil, &persistedCorrelation, &takeoverCount,
		)
		if scanErr == sql.ErrNoRows {
			return fmt.Errorf("controlplane: RIL action reservation window expired or unavailable")
		}
		if scanErr != nil {
			return scanErr
		}
		if executionID != request.ExecutionID || requestDigest != request.RequestDigest || admissionDigest != request.AdmissionDigest ||
			(persistedCorrelation != "" && persistedCorrelation != request.AuditCorrelationID) {
			result = rilaction.LedgerReservation{Disposition: rilaction.LedgerConflict}
			return nil
		}
		if status == "in-progress" {
			var databaseNow time.Time
			if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
				return err
			}
			if !persistedValidUntil.After(databaseNow) {
				if !validUntil.After(databaseNow) {
					return fmt.Errorf("controlplane: takeover window already expired")
				}
				var acquiredToken string
				var acquiredCount int64
				if err := tx.QueryRowContext(ctx, `
					UPDATE ril_action_execution_ledger
					SET reservation_token = $3, requested_at = $4, valid_until = $5,
					    audit_correlation_id = $6, takeover_count = takeover_count + 1,
					    updated_at = clock_timestamp()
					WHERE tenant_id = $1 AND idempotency_key = $2
					  AND status = 'in-progress' AND valid_until <= $7
					RETURNING reservation_token, takeover_count
				`, request.TenantID, request.IdempotencyKey, token, requestedAt, validUntil,
					request.AuditCorrelationID, databaseNow).Scan(&acquiredToken, &acquiredCount); err != nil {
					return err
				}
				if err := appendRILExecutionLeaseAudit(ctx, tx, request, acquiredToken, "taken-over", acquiredCount); err != nil {
					return err
				}
				result = rilaction.LedgerReservation{Disposition: rilaction.LedgerAcquired, ReservationToken: acquiredToken}
				return nil
			}
			result = rilaction.LedgerReservation{Disposition: rilaction.LedgerInProgress}
			return nil
		}
		if status != "completed" || evidenceJSON == "" {
			return fmt.Errorf("controlplane: invalid persisted RIL action ledger state")
		}
		evidence, err := decodePersistedRILActionEvidence(evidenceJSON)
		if err != nil {
			return fmt.Errorf("controlplane: decode persisted RIL action evidence: %w", err)
		}
		result = rilaction.LedgerReservation{Disposition: rilaction.LedgerReplay, Evidence: evidence}
		return nil
	})
	if err != nil {
		return rilaction.LedgerReservation{}, err
	}
	return result, nil
}

func parseCanonicalLedgerTime(value string) (time.Time, error) {
	if !strings.HasSuffix(value, "Z") {
		return time.Time{}, fmt.Errorf("UTC Z timestamp required")
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC || parsed.Format(time.RFC3339Nano) != value {
		return time.Time{}, fmt.Errorf("canonical RFC3339Nano timestamp required")
	}
	return parsed, nil
}

func decodePersistedRILActionEvidence(value string) (*rilaction.Evidence, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(value))
	decoder.DisallowUnknownFields()
	var evidence rilaction.Evidence
	if err := decoder.Decode(&evidence); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values are not allowed")
		}
		return nil, err
	}
	return &evidence, nil
}

// Complete token-fences the final public-safe evidence commit.
func (s *PostgresStore) Complete(ctx context.Context, completion rilaction.LedgerCompletion) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("controlplane: database not configured")
	}
	if completion.Evidence.TenantID != completion.TenantID || completion.Evidence.ExecutionID != completion.ExecutionID ||
		completion.Evidence.RequestDigest != completion.RequestDigest ||
		strings.TrimSpace(completion.AdmissionDigest) == "" ||
		strings.TrimSpace(completion.AuditCorrelationID) == "" ||
		(completion.Evidence.Status != "succeeded" && completion.Evidence.Status != "failed") {
		return fmt.Errorf("controlplane: RIL action completion evidence binding invalid")
	}
	evidenceJSON, err := json.Marshal(completion.Evidence)
	if err != nil {
		return fmt.Errorf("controlplane: encode RIL action completion evidence: %w", err)
	}
	return s.withTenant(ctx, completion.TenantID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE ril_action_execution_ledger
			SET status = 'completed', evidence_json = $7::jsonb,
				completed_at = now(), updated_at = now()
			WHERE tenant_id = $1 AND idempotency_key = $2 AND execution_id = $3
				AND request_digest = $4 AND execution_admission_digest = $5
				AND reservation_token = $6
				AND audit_correlation_id = $8
				AND status = 'in-progress' AND evidence_json IS NULL
		`, completion.TenantID, completion.IdempotencyKey, completion.ExecutionID,
			completion.RequestDigest, completion.AdmissionDigest, completion.ReservationToken, evidenceJSON,
			completion.AuditCorrelationID)
		if err != nil {
			return err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows != 1 {
			return fmt.Errorf("controlplane: stale or conflicting RIL action completion")
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO ril_execution_lease_audit (
				tenant_id, idempotency_key, execution_id, event_type,
				reservation_token, audit_correlation_id, takeover_count
			)
			SELECT tenant_id, idempotency_key, execution_id, 'completed',
			       reservation_token, audit_correlation_id, takeover_count
			FROM ril_action_execution_ledger
			WHERE tenant_id = $1 AND idempotency_key = $2
		`, completion.TenantID, completion.IdempotencyKey)
		return err
	})
}

func appendRILExecutionLeaseAudit(ctx context.Context, tx *sql.Tx, request rilaction.LedgerReservationRequest, token, eventType string, takeoverCount int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO ril_execution_lease_audit (
			tenant_id, idempotency_key, execution_id, event_type,
			reservation_token, audit_correlation_id, takeover_count
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, request.TenantID, request.IdempotencyKey, request.ExecutionID, eventType,
		token, request.AuditCorrelationID, takeoverCount)
	return err
}

var _ rilaction.ExecutionLedger = (*PostgresStore)(nil)
