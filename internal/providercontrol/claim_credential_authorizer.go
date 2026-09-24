package providercontrol

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// claimCredentialAuthorizer is the ledger-internal seam which revalidates the
// exact credential custody selected at admission before a new side-effecting
// claim is created or taken over. The caller owns the transaction.
type claimCredentialAuthorizer interface {
	AuthorizeSideEffectingClaimTx(context.Context, *sql.Tx, OperationRecord) error
}

type postgresClaimCredentialAuthorizer struct{}

var _ claimCredentialAuthorizer = postgresClaimCredentialAuthorizer{}

func (l *PostgresLedger) authorizeClaimCredentialTx(
	ctx context.Context,
	tx *sql.Tx,
	record OperationRecord,
	access ExecutionClaimAccess,
) error {
	if access == ExecutionClaimReadOnly {
		return nil
	}
	if access != ExecutionClaimSideEffecting || l == nil || l.claimCredentials == nil {
		return ErrClaimCredentialDenied
	}
	return l.claimCredentials.AuthorizeSideEffectingClaimTx(ctx, tx, record)
}

func (postgresClaimCredentialAuthorizer) AuthorizeSideEffectingClaimTx(
	ctx context.Context,
	tx *sql.Tx,
	record OperationRecord,
) error {
	if tx == nil {
		return fmt.Errorf("%w: credential authorization transaction is required", ErrClaimCredentialDenied)
	}
	command := record.Command
	if record.ExecutionProfile.CredentialMode == CredentialModeWorkerHeld {
		if err := authorizeSubstrateClaimTx(ctx, tx, record); err != nil {
			return err
		}
	}
	var (
		row           credentialHandleRow
		mode          string
		storedCustody sql.NullString
		storedConnect sql.NullString
		validFrom     sql.NullTime
		validUntil    sql.NullTime
		revokedAt     sql.NullTime
	)
	// This is the typed early denial. Migration 033 makes the claim trigger the
	// non-invocable SECURITY DEFINER boundary which locks and revalidates this
	// exact credential immediately before the claim can commit. A direct row
	// lock here would incorrectly require UPDATE on credential authority.
	err := tx.QueryRowContext(ctx, `
SELECT
    handle_id,
    handle_version,
    provider_id,
    credential_mode,
    subject_kind,
    subject_id,
    grant_id,
    credential_scope,
    custody_ref,
    connection_ref,
    custody_hash,
    connection_hash,
    valid_from,
    valid_until,
    revoked_at
FROM provider_credential_handles
WHERE tenant_id = $1
  AND provider_id = $2
  AND credential_mode = $3
  AND custody_ref = $4
  AND connection_ref = $5`,
		command.TenantID,
		command.ProviderID,
		string(record.ExecutionProfile.CredentialMode),
		command.CustodyRef,
		command.ConnectionRef,
	).Scan(
		&row.HandleID,
		&row.HandleVersion,
		&row.ProviderID,
		&mode,
		&row.SubjectKind,
		&row.SubjectID,
		&row.GrantID,
		&row.Scope,
		&row.CustodyRef,
		&row.ConnectionRef,
		&storedCustody,
		&storedConnect,
		&validFrom,
		&validUntil,
		&revokedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrClaimCredentialDenied
	}
	if err != nil {
		return fmt.Errorf("%w: credential custody lookup failed", ErrClaimCredentialDenied)
	}
	var databaseTime time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&databaseTime); err != nil {
		return fmt.Errorf("%w: credential authorization time lookup failed", ErrClaimCredentialDenied)
	}

	row.CredentialMode = CredentialMode(strings.TrimSpace(mode))
	row.HandleID = strings.TrimSpace(row.HandleID)
	row.ProviderID = strings.TrimSpace(row.ProviderID)
	row.SubjectKind = strings.TrimSpace(row.SubjectKind)
	row.SubjectID = strings.TrimSpace(row.SubjectID)
	row.GrantID = strings.TrimSpace(row.GrantID)
	row.Scope = strings.TrimSpace(row.Scope)
	row.CustodyRef = strings.TrimSpace(row.CustodyRef)
	row.ConnectionRef = strings.TrimSpace(row.ConnectionRef)
	if !validClaimCredentialRow(
		command.TenantID,
		row,
		record.ExecutionProfile.CredentialMode,
		storedCustody,
		storedConnect,
		validFrom,
		validUntil,
		revokedAt,
		databaseTime,
		command.CustodyHash,
		command.ConnectionHash,
	) {
		return ErrClaimCredentialDenied
	}
	return nil
}

func validClaimCredentialRow(
	tenantID string,
	row credentialHandleRow,
	credentialMode CredentialMode,
	storedCustody sql.NullString,
	storedConnection sql.NullString,
	validFrom sql.NullTime,
	validUntil sql.NullTime,
	revokedAt sql.NullTime,
	databaseTime time.Time,
	commandCustodyHash string,
	commandConnectionHash string,
) bool {
	if !liveClaimCredentialRow(tenantID, row, credentialMode, storedCustody, storedConnection, validFrom, validUntil, revokedAt, databaseTime) {
		return false
	}
	custodyHash, custodyErr := credentialCustodyHash(tenantID, row)
	connectionHash, connectionErr := credentialConnectionHash(tenantID, row)
	return custodyErr == nil && connectionErr == nil &&
		storedCustody.String == custodyHash && storedConnection.String == connectionHash &&
		commandCustodyHash == custodyHash && commandConnectionHash == connectionHash
}

func liveClaimCredentialRow(
	tenantID string,
	row credentialHandleRow,
	credentialMode CredentialMode,
	storedCustody, storedConnection sql.NullString,
	validFrom, validUntil, revokedAt sql.NullTime,
	databaseTime time.Time,
) bool {
	return tenantID != "" && row.ProviderID != "" && row.CredentialMode == credentialMode &&
		validCredentialHandleIdentity(row) && !revokedAt.Valid && storedCustody.Valid && storedConnection.Valid &&
		validFrom.Valid && validUntil.Valid && !databaseTime.IsZero() &&
		!databaseTime.Before(validFrom.Time) && databaseTime.Before(validUntil.Time)
}
