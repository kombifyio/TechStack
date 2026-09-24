package providercontrol

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestNativeAdmissionIntegrationRestrictedRoleUsesOnlyBoundedTenantDiscovery(t *testing.T) {
	adminDatabase := openNativeAdmissionIntegrationDB(t)
	seedNativeAdmissionCredentialHandle(t, adminDatabase)
	runtimeDatabase := openRestrictedNativeAdmissionRuntimeDB(t, adminDatabase)

	var runtimeRole, schemaName string
	if err := runtimeDatabase.QueryRowContext(t.Context(), `SELECT current_user, current_schema()`).Scan(
		&runtimeRole,
		&schemaName,
	); err != nil {
		t.Fatalf("load restricted runtime identity: %v", err)
	}
	quotedRole := pgx.Identifier{runtimeRole}.Sanitize()
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	for _, function := range []string{
		"provider_control_list_runnable_tenants(text,integer)",
		"provider_control_runtime_authority()",
	} {
		// #nosec G202 -- schema/role identifiers are read from the isolated test
		// connections and quoted; function signatures are fixed constants.
		statement := "GRANT EXECUTE ON FUNCTION " + quotedSchema + "." + function + " TO " + quotedRole
		if _, err := adminDatabase.ExecContext(t.Context(), statement); err != nil {
			t.Fatalf("grant bounded runtime function %s: %v", function, err)
		}
	}

	profiles, err := NewPostgresCatalogExecutionProfileResolver(runtimeDatabase)
	if err != nil {
		t.Fatalf("create restricted catalog resolver: %v", err)
	}
	admission, _ := newNativeAdmissionIntegrationServiceWithResolver(
		t,
		runtimeDatabase,
		profiles,
		staticAdmissionCapacityPolicy{grant: ownerLimitedCapacityGrant("owner-1", 100)},
	)
	request := nativeProvisionAdmissionFixture()
	request.Lease.Subject.ID = request.TenantID
	request.Lease.Metadata = map[string]string{runtimeOfferingMetadataKey: "monthly-runtime-standard"}
	if _, err := admission.AdmitProvision(t.Context(), request); err != nil {
		t.Fatalf("restricted native admission: %v", err)
	}

	ledger, err := NewPostgresLedger(runtimeDatabase, nil)
	if err != nil {
		t.Fatalf("NewPostgresLedger: %v", err)
	}
	page, err := ledger.ListRunnableTenants(t.Context(), "", 10)
	if err != nil {
		t.Fatalf("bounded tenant discovery: %v", err)
	}
	if len(page.TenantIDs) != 1 || page.TenantIDs[0] != request.TenantID || page.NextCursor != "" {
		t.Fatalf("bounded tenant page = %#v, want only %q", page, request.TenantID)
	}
	if _, err := runtimeDatabase.ExecContext(t.Context(), `
		SELECT tenant_id
		FROM provider_control_list_runnable_tenants('', NULL)
	`); err == nil {
		t.Fatal("NULL runnable-tenant limit bypassed the hard bound")
	}

	var rawOperationCount int
	if err := runtimeDatabase.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM provider_operations`).Scan(&rawOperationCount); err != nil {
		t.Fatalf("raw FORCE-RLS operation read: %v", err)
	}
	if rawOperationCount != 0 {
		t.Fatalf("unscoped runtime role saw %d raw provider operations", rawOperationCount)
	}
	if _, err := runtimeDatabase.ExecContext(t.Context(), `
		INSERT INTO provider_control_runnable_tenants (tenant_id, runnable_operation_count)
		VALUES ('injected-tenant', 1)
	`); err == nil {
		t.Fatal("runtime role injected a raw runnable-tenant directory row")
	}
	if _, err := runtimeDatabase.QueryContext(t.Context(), `
		SELECT tenant_id
		FROM provider_incident_list_tenant_ids()
	`); err == nil {
		t.Fatal("runtime role executed provider incident tenant enumerator")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("runtime incident tenant enumerator error = %v, want SQLSTATE 42501", err)
		}
	}

	for _, function := range []struct {
		name    string
		allowed bool
	}{
		{name: fmt.Sprintf("%s.provider_control_list_runnable_tenants(text,integer)", quotedSchema), allowed: true},
		{name: fmt.Sprintf("%s.provider_control_runtime_authority()", quotedSchema), allowed: true},
		{name: fmt.Sprintf("%s.provider_incident_list_tenant_ids()", quotedSchema), allowed: false},
		{name: fmt.Sprintf("%s.provider_execution_immutable_update()", quotedSchema), allowed: false},
		{name: fmt.Sprintf("%s.provider_execution_claim_credential_guard()", quotedSchema), allowed: false},
	} {
		var allowed bool
		if err := runtimeDatabase.QueryRowContext(t.Context(), `
			SELECT pg_catalog.has_function_privilege(current_user, $1, 'EXECUTE')
		`, function.name).Scan(&allowed); err != nil {
			t.Fatalf("inspect function privilege %s: %v", function.name, err)
		}
		if allowed != function.allowed {
			t.Fatalf("function %s executable = %v, want %v", function.name, allowed, function.allowed)
		}
	}
}
