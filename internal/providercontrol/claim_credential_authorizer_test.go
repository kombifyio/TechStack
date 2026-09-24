package providercontrol

import (
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

func TestClassifyExecutionClaimAccess(t *testing.T) {
	tests := []struct {
		name      string
		operation providerexecutor.Operation
		phase     providerexecutor.Phase
		want      ExecutionClaimAccess
		wantErr   bool
	}{
		{"plan", providerexecutor.OperationPlan, providerexecutor.PhaseAccepted, ExecutionClaimReadOnly, false},
		{"observe", providerexecutor.OperationObserve, providerexecutor.PhaseAccepted, ExecutionClaimReadOnly, false},
		{"provision dispatch", providerexecutor.OperationProvision, providerexecutor.PhaseAccepted, ExecutionClaimSideEffecting, false},
		{"provision presence", providerexecutor.OperationProvision, providerexecutor.PhaseResourcesBound, ExecutionClaimReadOnly, false},
		{"reconcile", providerexecutor.OperationReconcile, providerexecutor.PhaseAccepted, ExecutionClaimSideEffecting, false},
		{"decommission delete", providerexecutor.OperationDecommission, providerexecutor.PhaseAccepted, ExecutionClaimSideEffecting, false},
		{"decommission accepted poll", providerexecutor.OperationDecommission, providerexecutor.PhaseDeleteAccepted, ExecutionClaimReadOnly, false},
		{"decommission absence poll", providerexecutor.OperationDecommission, providerexecutor.PhaseAbsencePending, ExecutionClaimReadOnly, false},
		{"unknown provision phase", providerexecutor.OperationProvision, providerexecutor.PhasePresent, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := classifyExecutionClaimAccess(tt.operation, tt.phase)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("classifyExecutionClaimAccess() = %q, %v; want %q, err=%v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestPostgresClaimCredentialAuthorizerRequiresExactLiveHashes(t *testing.T) {
	handle := testCredentialHandle()
	custodyHash, _ := credentialCustodyHash("tenant-1", handle)
	connectionHash, _ := credentialConnectionHash("tenant-1", handle)
	dbAt := contractNow

	tests := []struct {
		name             string
		storedCustody    string
		storedConnection string
		validFrom        time.Time
		validUntil       time.Time
		revokedAt        any
		wantErr          bool
	}{
		{"authorized", custodyHash, connectionHash, dbAt.Add(-time.Hour), dbAt.Add(time.Hour), nil, false},
		{"expired", custodyHash, connectionHash, dbAt.Add(-2 * time.Hour), dbAt.Add(-time.Hour), nil, true},
		{"revoked", custodyHash, connectionHash, dbAt.Add(-time.Hour), dbAt.Add(time.Hour), dbAt.Add(-time.Minute), true},
		{"custody digest drift", digest("wrong"), connectionHash, dbAt.Add(-time.Hour), dbAt.Add(time.Hour), nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock.New: %v", err)
			}
			defer func() { _ = db.Close() }()
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta("FROM provider_credential_handles")).
				WithArgs("tenant-1", "ionos", "managed", handle.CustodyRef, handle.ConnectionRef).
				WillReturnRows(claimCredentialRows().AddRow(
					handle.HandleID, handle.HandleVersion, handle.ProviderID, handle.CredentialMode,
					handle.SubjectKind, handle.SubjectID, handle.GrantID, handle.Scope,
					handle.CustodyRef, handle.ConnectionRef, tt.storedCustody, tt.storedConnection,
					tt.validFrom, tt.validUntil, tt.revokedAt,
				))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT clock_timestamp()")).
				WillReturnRows(sqlmock.NewRows([]string{"database_time"}).AddRow(dbAt))
			mock.ExpectRollback()

			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatalf("BeginTx: %v", err)
			}
			record := OperationRecord{
				ExecutionProfile: ExecutionProfileSnapshot{CredentialMode: CredentialModeManaged},
				Command: providerexecutor.Command{
					TenantID: "tenant-1", ProviderID: "ionos",
					CustodyRef: handle.CustodyRef, CustodyHash: custodyHash,
					ConnectionRef: handle.ConnectionRef, ConnectionHash: connectionHash,
				},
			}
			err = (postgresClaimCredentialAuthorizer{}).AuthorizeSideEffectingClaimTx(t.Context(), tx, record)
			if (err != nil) != tt.wantErr || (err != nil && !errors.Is(err, ErrClaimCredentialDenied)) {
				t.Fatalf("AuthorizeSideEffectingClaimTx error = %v, want denied=%v", err, tt.wantErr)
			}
			_ = tx.Rollback()
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("sql expectations: %v", err)
			}
		})
	}
}

func TestPostgresClaimCredentialAuthorizerSanitizesDatabaseFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("FROM provider_credential_handles")).
		WillReturnError(errors.New("secret backend detail must not escape"))
	mock.ExpectRollback()
	tx, err := db.BeginTx(t.Context(), &sql.TxOptions{})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	record := OperationRecord{
		ExecutionProfile: ExecutionProfileSnapshot{CredentialMode: CredentialModeManaged},
		Command: providerexecutor.Command{
			TenantID: "tenant-1", ProviderID: "ionos",
			CustodyRef:    "custody://tenant-1/provider-access",
			ConnectionRef: "provider-connection://tenant-1/managed-compute",
		},
	}
	err = (postgresClaimCredentialAuthorizer{}).AuthorizeSideEffectingClaimTx(t.Context(), tx, record)
	if !errors.Is(err, ErrClaimCredentialDenied) || regexp.MustCompile(`secret backend detail`).MatchString(err.Error()) {
		t.Fatalf("sanitized error = %v", err)
	}
	_ = tx.Rollback()
}

func claimCredentialRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"handle_id", "handle_version", "provider_id", "credential_mode", "subject_kind",
		"subject_id", "grant_id", "credential_scope", "custody_ref", "connection_ref",
		"custody_hash", "connection_hash", "valid_from", "valid_until", "revoked_at",
	})
}
