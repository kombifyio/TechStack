package providercontrol

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresLedgerListsRunnableTenantPageWithKeysetCursor(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ledger := &PostgresLedger{db: database}
	mock.ExpectQuery(`FROM provider_control_list_runnable_tenants\(\$1, \$2\)`).
		WithArgs("tenant-before", 3).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).
			AddRow("tenant-c").AddRow("tenant-d").AddRow("tenant-e"))

	page, err := ledger.ListRunnableTenants(t.Context(), " tenant-before ", 2)
	if err != nil {
		t.Fatalf("ListRunnableTenants: %v", err)
	}
	if len(page.TenantIDs) != 2 || page.TenantIDs[0] != "tenant-c" || page.TenantIDs[1] != "tenant-d" {
		t.Fatalf("tenant page = %#v", page)
	}
	if page.NextCursor != "tenant-d" {
		t.Fatalf("next cursor = %q, want tenant-d", page.NextCursor)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresLedgerRunnableTenantFinalPageClearsCursor(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ledger := &PostgresLedger{db: database}
	mock.ExpectQuery(`FROM provider_control_list_runnable_tenants\(\$1, \$2\)`).
		WithArgs("", 3).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("tenant-a"))
	page, err := ledger.ListRunnableTenants(t.Context(), "", 2)
	if err != nil {
		t.Fatalf("ListRunnableTenants: %v", err)
	}
	if page.NextCursor != "" || len(page.TenantIDs) != 1 {
		t.Fatalf("final tenant page = %#v", page)
	}
}

func TestPostgresLedgerRunnableTenantDiscoveryFailsClosed(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ledger := &PostgresLedger{db: database}
	want := errors.New("permission denied for relation provider_operations")
	mock.ExpectQuery(`FROM provider_control_list_runnable_tenants\(\$1, \$2\)`).WillReturnError(want)
	if _, err := ledger.ListRunnableTenants(t.Context(), "", 1); !errors.Is(err, want) {
		t.Fatalf("ListRunnableTenants error = %v, want %v", err, want)
	}
}

func TestPostgresLedgerListsDueProviderDecommissionTenantPage(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ledger := &PostgresLedger{db: database}
	mock.ExpectQuery(`FROM provider_control_list_due_decommission_wait_tenants\(\$1, \$2\)`).
		WithArgs("tenant-before", 3).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).
			AddRow("tenant-c").AddRow("tenant-d").AddRow("tenant-e"))

	page, err := ledger.ListDueProviderDecommissionTenants(t.Context(), " tenant-before ", 2)
	if err != nil {
		t.Fatalf("ListDueProviderDecommissionTenants: %v", err)
	}
	if len(page.TenantIDs) != 2 || page.TenantIDs[0] != "tenant-c" || page.TenantIDs[1] != "tenant-d" {
		t.Fatalf("tenant page = %#v", page)
	}
	if page.NextCursor != "tenant-d" {
		t.Fatalf("next cursor = %q, want tenant-d", page.NextCursor)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresLedgerListsProviderProvisionWaitOperationReferences(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ledger := &PostgresLedger{db: database}
	mock.ExpectQuery(`FROM provider_control_list_provider_provision_waits\(\$1, \$2, \$3\)`).
		WithArgs("tenant-before", "operation-before", 3).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "operation_id"}).
			AddRow("tenant-a", "operation-a").
			AddRow("tenant-b", "operation-b").
			AddRow("tenant-c", "operation-c"))

	page, err := ledger.ListProviderProvisionWaits(t.Context(), " tenant-before\x00operation-before ", 2)
	if err != nil {
		t.Fatalf("ListProviderProvisionWaits: %v", err)
	}
	if len(page.Operations) != 2 || page.Operations[0].TenantID != "tenant-a" ||
		page.Operations[1].OperationID != "operation-b" {
		t.Fatalf("provider provision wait page = %#v", page)
	}
	if page.NextCursor != "tenant-b\x00operation-b" {
		t.Fatalf("next cursor = %q", page.NextCursor)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
