package providercontrol

import (
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresLedgerReadManagedRuntimeCleanupUsesExactTenantGenerationFacts(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ledger := &PostgresLedger{db: database}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("SELECT set_config($1, $2, true)")).
		WithArgs(tenantContextKey, "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)server\.lifecycle_state = 'decommissioned'.*server\.desired_state = 'absent'.*server\.decommissioned_at IS NOT NULL.*operation\.phase IN \('absent', 'failed', 'denied'\).*FROM techstack_vm_leases AS lease.*managed_runtime_capacity_release_facts AS release.*provider_absence_observations AS observation`).
		WithArgs("tenant-1", "lease-1", string(ExecutionAuthorityTechstackProviderControl), operationEnvelopeVersion).
		WillReturnRows(sqlmock.NewRows([]string{
			"server_bound", "server_terminal", "provider_operation_found", "provider_operation_terminal",
			"absence_evidence_ref", "capacity_released",
		}).AddRow(true, true, true, true, "provider-evidence://ionos/opaque-proof", true))
	mock.ExpectCommit()

	facts, err := ledger.ReadManagedRuntimeCleanup(t.Context(), "tenant-1", "lease-1")
	if err != nil {
		t.Fatalf("ReadManagedRuntimeCleanup: %v", err)
	}
	if facts == nil || !facts.ServerBound || !facts.ServerTerminal || !facts.ProviderOperationFound ||
		!facts.ProviderOperationTerminal || !facts.CapacityReleased ||
		facts.AbsenceEvidenceRef != "provider-evidence://ionos/opaque-proof" {
		t.Fatalf("facts = %+v", facts)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}
