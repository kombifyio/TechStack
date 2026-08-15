package main

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kombifyio/techstack/pkg/db"
)

func TestProviderControlRuntimeRolePostureQueryPinsBoundedAuthority(t *testing.T) {
	for _, required := range []string{
		"active_schema.nspname = current_schema()",
		"provider_control_runtime_authority() AS physical_authority",
		"physical_authority.system_identifier",
		"provider_control_lock_runtime_lease_projection(text)",
		"provider_control_count_unsettled_generation_dispatch_guards(text,uuid)",
		"provider_control_list_runnable_tenants(text,integer)",
		"provider_control_list_due_decommission_wait_tenants(text,integer)",
		"provider_control_list_provider_provision_waits(text,text,integer)",
		"provider_control_list_stale_capacity_recovery_candidates(text,text,integer)",
		"provider_incident_refresh_pending_dispatch_tenant",
		"provider_incident_list_tenant_ids",
		"required_tenant_rls",
		"object.relrowsecurity",
		"object.relforcerowsecurity",
		"has_excess_table_authority",
		"has_excess_sequence_authority",
		"has_excess_function_authority",
		"malformed_security_definer_boundary",
		"search_path=pg_catalog, %I, pg_temp",
		"pg_catalog.aclexplode",
		"pg_catalog.pg_attribute",
		"pg_catalog.pg_depend",
		"dependency.deptype = 'e'",
		"NOT acl.is_grantable",
	} {
		if !strings.Contains(providerControlRuntimeRolePostureQuery, required) {
			t.Fatalf("posture query is missing bounded-authority invariant %q", required)
		}
	}
	if regexp.MustCompile(`\('[^']+', '[A-Z]+,[A-Z]`).MatchString(providerControlRuntimeRolePostureQuery) {
		t.Fatal("required capability rows must prove one table privilege at a time")
	}
	if !strings.Contains(providerControlRuntimeRolePostureQuery, "pg_has_role(runtime_role.oid, granted_role.oid, 'MEMBER')") ||
		strings.Contains(providerControlRuntimeRolePostureQuery, "granted_role.rolsuper") {
		t.Fatal("posture must reject every SET ROLE membership, not only inherited cluster attributes")
	}
	for _, adminOnlyTable := range []string{
		"provider_provision_discovery_observations",
		"provider_control_runnable_tenants",
		"provider_incident_pending_dispatch_tenants",
	} {
		if strings.Contains(providerControlRuntimeRolePostureQuery, "('"+adminOnlyTable+"', '") {
			t.Fatalf("runtime role must not require raw table authority on %q", adminOnlyTable)
		}
	}
	if count := strings.Count(
		providerControlRuntimeRolePostureQuery,
		"('provider_provision_resolution_decisions', 'SELECT')",
	); count != 1 {
		t.Fatalf("provider resolution decision SELECT posture count = %d, want 1", count)
	}
	for _, forbiddenPrivilege := range []string{"INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"} {
		if strings.Contains(
			providerControlRuntimeRolePostureQuery,
			"('provider_provision_resolution_decisions', '"+forbiddenPrivilege+"')",
		) {
			t.Fatalf("runtime posture grants provider resolution decisions %s", forbiddenPrivilege)
		}
	}
}

func TestProviderControlRuntimeDatabaseConfigRequiresCanonicalExplicitURL(t *testing.T) {
	t.Setenv(providerControlRuntimeDatabaseURLEnv, "")
	t.Setenv("ADMIN_DATABASE_URL", "postgres://must-not-fallback")
	t.Setenv("TECHSTACK_RUNTIME_DATABASE_URL", "postgres://must-not-fallback")
	t.Setenv("DATABASE_URL", "postgres://must-not-fallback")

	_, err := providerControlRuntimeDatabaseConfigFromEnv()
	if !errors.Is(err, errProviderControlRuntimeDatabaseNotConfigured) {
		t.Fatalf("config error = %v, want not-configured error", err)
	}
}

func TestProviderControlRuntimeDatabaseConfigUsesOnlyCanonicalExplicitURL(t *testing.T) {
	const runtimeURL = "postgres://techstack_provider_control_runtime:secret@provider-runtime/techstack"
	t.Setenv(providerControlRuntimeDatabaseURLEnv, "  "+runtimeURL+"  ")
	t.Setenv("ADMIN_DATABASE_URL", "postgres://admin")
	t.Setenv("DATABASE_URL", "postgres://migration")

	cfg, err := providerControlRuntimeDatabaseConfigFromEnv()
	if err != nil {
		t.Fatalf("providerControlRuntimeDatabaseConfigFromEnv: %v", err)
	}
	if cfg.DSN != runtimeURL {
		t.Fatalf("DSN = %q, want canonical provider runtime URL", cfg.DSN)
	}
}

func TestProviderControlRuntimeDatabaseConfigRejectsSessionAuthorityOverrides(t *testing.T) {
	for _, rawURL := range []string{
		"postgres://admin:secret@localhost/techstack?role=techstack_provider_control_runtime",
		"postgres://techstack_provider_control_runtime:secret@localhost/techstack?session_authorization=admin",
		"postgres://techstack_provider_control_runtime:secret@localhost/techstack?options=-c%20role%3Dadmin",
	} {
		t.Run(rawURL, func(t *testing.T) {
			t.Setenv(providerControlRuntimeDatabaseURLEnv, rawURL)
			if _, err := providerControlRuntimeDatabaseConfigFromEnv(); err == nil {
				t.Fatal("session-authority override was accepted")
			}
		})
	}
}

func TestProviderControlRuntimeRolePostureHasExactCapacityRights(t *testing.T) {
	matches := regexp.MustCompile(`\('managed_runtime_capacity_reservations', '([A-Z]+)'\)`).FindAllStringSubmatch(providerControlRuntimeRolePostureQuery, -1)
	if len(matches) != 2 || matches[0][1] != "SELECT" || matches[1][1] != "INSERT" {
		t.Fatalf("capacity reservation rights = %#v, want only SELECT and INSERT", matches)
	}
	if !strings.Contains(providerControlRuntimeRolePostureQuery, "pg_catalog.acldefault('r', object.relowner)") ||
		!strings.Contains(providerControlRuntimeRolePostureQuery, "allowed.privilege = acl.privilege_type") {
		t.Fatal("generic excess-authority detector is missing")
	}
}

func TestProviderControlRuntimeRolePostureIncludesExactIncidentAndWorkflowRunCapabilities(t *testing.T) {
	exact := map[string][]string{
		"provider_incidents": {"SELECT", "INSERT"},
		"ril_workflow_runs":  {"SELECT", "INSERT"},
	}
	for table, privileges := range exact {
		for _, privilege := range privileges {
			needle := "('" + table + "', '" + privilege + "')"
			if count := strings.Count(providerControlRuntimeRolePostureQuery, needle); count != 1 {
				t.Fatalf("posture capability %s appears %d times, want 1", needle, count)
			}
		}
	}
	for _, forbidden := range []struct {
		table     string
		privilege string
	}{
		{table: "provider_incident_advisory_attempts", privilege: "UPDATE"},
		{table: "provider_incident_advisory_attempts", privilege: "DELETE"},
		{table: "provider_incidents", privilege: "UPDATE"},
		{table: "provider_incident_advisory_attempts", privilege: "SELECT"},
		{table: "provider_incident_advisory_attempts", privilege: "INSERT"},
		{table: "ril_workflow_runs", privilege: "UPDATE"},
		{table: "ril_workflow_steps", privilege: "SELECT"},
		{table: "ril_workflow_steps", privilege: "INSERT"},
		{table: "ril_workflow_steps", privilege: "UPDATE"},
		{table: "ril_workflow_timers", privilege: "SELECT"},
		{table: "ril_workflow_timers", privilege: "INSERT"},
		{table: "ril_workflow_timers", privilege: "UPDATE"},
		{table: "provider_incidents", privilege: "DELETE"},
		{table: "ril_workflow_runs", privilege: "DELETE"},
	} {
		needle := "('" + forbidden.table + "', '" + forbidden.privilege + "')"
		if strings.Contains(providerControlRuntimeRolePostureQuery, needle) {
			t.Fatalf("posture query grants forbidden capability %s", needle)
		}
	}
}

func TestVerifyProviderControlRuntimeRolePostureAcceptsSeparatedLeastPrivilegeRole(t *testing.T) {
	runtimeDB, runtimeMock, migrationDB, migrationMock := newProviderDatabaseMocks(t)
	fixture := validProviderRuntimePostureFixture()
	expectProviderRuntimePosture(runtimeMock, fixture)
	expectMigrationIdentity(migrationMock, providerControlDatabaseIdentity{
		roleOID: 100, roleName: "migration_owner", sessionRoleOID: 100, sessionRoleName: "migration_owner", systemIdentifier: fixture.identity.systemIdentifier,
		databaseOID: 42, databaseName: "techstack", schemaOID: 84, schemaName: "authority", authorityOwnerOID: 100,
	})

	if err := verifyProviderControlRuntimeRolePosture(context.Background(), runtimeDB, migrationDB); err != nil {
		t.Fatalf("verify posture: %v", err)
	}
	assertProviderDatabaseExpectations(t, runtimeMock, migrationMock)
}

func TestVerifyProviderControlRuntimeRolePostureRejectsIdentityMismatches(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*providerControlRuntimeRolePosture, *providerControlDatabaseIdentity)
		wantError string
	}{
		{name: "wrong runtime role", mutate: func(runtime *providerControlRuntimeRolePosture, _ *providerControlDatabaseIdentity) {
			runtime.identity.roleName = "provider_runtime_alias"
		}, wantError: "runtime role must be"},
		{name: "runtime session role", mutate: func(runtime *providerControlRuntimeRolePosture, _ *providerControlDatabaseIdentity) {
			runtime.identity.sessionRoleOID = 999
		}, wantError: "authenticated session role"},
		{name: "migration session role", mutate: func(_ *providerControlRuntimeRolePosture, migration *providerControlDatabaseIdentity) {
			migration.sessionRoleName = "migration_admin"
		}, wantError: "authenticated session role"},
		{name: "physical cluster", mutate: func(runtime *providerControlRuntimeRolePosture, _ *providerControlDatabaseIdentity) {
			runtime.identity.systemIdentifier = "cluster-b"
		}, wantError: "same physical PostgreSQL cluster"},
		{name: "database", mutate: func(runtime *providerControlRuntimeRolePosture, _ *providerControlDatabaseIdentity) {
			runtime.identity.databaseOID = 43
		}, wantError: "same database"},
		{name: "schema", mutate: func(runtime *providerControlRuntimeRolePosture, _ *providerControlDatabaseIdentity) {
			runtime.identity.schemaOID = 85
		}, wantError: "trusted current schema"},
		{name: "same role", mutate: func(runtime *providerControlRuntimeRolePosture, migration *providerControlDatabaseIdentity) {
			runtime.identity.roleOID = migration.roleOID
			runtime.identity.sessionRoleOID = migration.roleOID
		}, wantError: "role OIDs must differ"},
		{name: "function owner", mutate: func(runtime *providerControlRuntimeRolePosture, _ *providerControlDatabaseIdentity) {
			runtime.identity.authorityOwnerOID = 999
		}, wantError: "owned by the migration role"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtimeDB, runtimeMock, migrationDB, migrationMock := newProviderDatabaseMocks(t)
			runtime := validProviderRuntimePostureFixture()
			migration := providerControlDatabaseIdentity{
				roleOID: 100, roleName: "migration_owner", sessionRoleOID: 100, sessionRoleName: "migration_owner", systemIdentifier: "cluster-a",
				databaseOID: 42, databaseName: "techstack", schemaOID: 84, schemaName: "authority", authorityOwnerOID: 100,
			}
			test.mutate(&runtime, &migration)
			expectProviderRuntimePosture(runtimeMock, runtime)
			expectMigrationIdentity(migrationMock, migration)

			err := verifyProviderControlRuntimeRolePosture(context.Background(), runtimeDB, migrationDB)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("verify error = %v, want %q", err, test.wantError)
			}
			assertProviderDatabaseExpectations(t, runtimeMock, migrationMock)
		})
	}
}

func TestVerifyProviderControlRuntimeRolePostureEnforcesExpectedDatabaseIdentityPin(t *testing.T) {
	tests := []struct {
		name      string
		pin       string
		wantError string
	}{
		{name: "name pin matches", pin: "techstack"},
		{name: "name and system identifier pin matches", pin: "techstack@cluster-a"},
		{name: "stale env contract", pin: "kombify_runtime", wantError: "env contract stale"},
		{name: "database moved", pin: "techstack@cluster-b", wantError: "database moved"},
		{name: "malformed pin", pin: "techstack@", wantError: "must be <database_name> or <database_name>@<postgres system_identifier>"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(db.EnvExpectedDatabaseIdentity, test.pin)
			runtimeDB, runtimeMock, migrationDB, migrationMock := newProviderDatabaseMocks(t)
			expectProviderRuntimePosture(runtimeMock, validProviderRuntimePostureFixture())
			expectMigrationIdentity(migrationMock, providerControlDatabaseIdentity{
				roleOID: 100, roleName: "migration_owner", sessionRoleOID: 100, sessionRoleName: "migration_owner", systemIdentifier: "cluster-a",
				databaseOID: 42, databaseName: "techstack", schemaOID: 84, schemaName: "authority", authorityOwnerOID: 100,
			})

			err := verifyProviderControlRuntimeRolePosture(context.Background(), runtimeDB, migrationDB)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("verify posture: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("verify error = %v, want %q", err, test.wantError)
			}
			assertProviderDatabaseExpectations(t, runtimeMock, migrationMock)
		})
	}
}

func TestVerifyProviderControlRuntimeRolePostureRejectsEveryForbiddenAuthority(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*providerControlRuntimeRolePosture)
		wantError string
	}{
		{name: "cluster privilege", mutate: func(p *providerControlRuntimeRolePosture) { p.superuser = true }, wantError: "forbidden cluster privileges"},
		{name: "role membership", mutate: func(p *providerControlRuntimeRolePosture) { p.setRoleMembership = true }, wantError: "forbidden cluster privileges"},
		{name: "object creation", mutate: func(p *providerControlRuntimeRolePosture) { p.untrustedObjectCreation = true }, wantError: "schema CREATE"},
		{name: "ownership", mutate: func(p *providerControlRuntimeRolePosture) { p.hasApplicationOwnership = true }, wantError: "ownership authority"},
		{name: "missing required", mutate: func(p *providerControlRuntimeRolePosture) { p.missingRequiredCapability = true }, wantError: "missing an exact"},
		{name: "excess table", mutate: func(p *providerControlRuntimeRolePosture) { p.hasExcessTableAuthority = true }, wantError: "outside the exact table, sequence, or function allowlist"},
		{name: "excess sequence", mutate: func(p *providerControlRuntimeRolePosture) { p.hasExcessSequenceAuthority = true }, wantError: "outside the exact table, sequence, or function allowlist"},
		{name: "excess function", mutate: func(p *providerControlRuntimeRolePosture) { p.hasExcessFunctionAuthority = true }, wantError: "outside the exact table, sequence, or function allowlist"},
		{name: "missing RLS", mutate: func(p *providerControlRuntimeRolePosture) { p.missingTenantRLSFence = true }, wantError: "missing FORCE RLS"},
		{name: "malformed SECURITY DEFINER", mutate: func(p *providerControlRuntimeRolePosture) { p.malformedSecurityDefinerFence = true }, wantError: "SECURITY DEFINER"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtimeDB, runtimeMock, migrationDB, migrationMock := newProviderDatabaseMocks(t)
			posture := validProviderRuntimePostureFixture()
			test.mutate(&posture)
			expectProviderRuntimePosture(runtimeMock, posture)
			expectMigrationIdentity(migrationMock, providerControlDatabaseIdentity{
				roleOID: 100, roleName: "migration_owner", sessionRoleOID: 100, sessionRoleName: "migration_owner", systemIdentifier: "cluster-a",
				databaseOID: 42, databaseName: "techstack", schemaOID: 84, schemaName: "authority", authorityOwnerOID: 100,
			})

			err := verifyProviderControlRuntimeRolePosture(context.Background(), runtimeDB, migrationDB)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("verify error = %v, want %q", err, test.wantError)
			}
			assertProviderDatabaseExpectations(t, runtimeMock, migrationMock)
		})
	}
}

func validProviderRuntimePostureFixture() providerControlRuntimeRolePosture {
	return providerControlRuntimeRolePosture{identity: providerControlDatabaseIdentity{
		roleOID: 200, roleName: providerControlRuntimeRoleName,
		sessionRoleOID: 200, sessionRoleName: providerControlRuntimeRoleName, systemIdentifier: "cluster-a",
		databaseOID: 42, databaseName: "techstack", schemaOID: 84, schemaName: "authority", authorityOwnerOID: 100,
	}}
}

func newProviderDatabaseMocks(t *testing.T) (*sql.DB, sqlmock.Sqlmock, *sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	runtimeDB, runtimeMock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtimeDB.Close() })
	migrationDB, migrationMock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrationDB.Close() })
	return runtimeDB, runtimeMock, migrationDB, migrationMock
}

func expectProviderRuntimePosture(mock sqlmock.Sqlmock, posture providerControlRuntimeRolePosture) {
	mock.ExpectQuery(regexp.QuoteMeta(providerControlRuntimeRolePostureQuery)).
		WillReturnRows(sqlmock.NewRows([]string{
			"role_oid", "role_name", "session_role_oid", "session_role_name", "system_identifier", "database_oid", "database_name", "schema_oid", "schema_name", "authority_owner_oid",
			"rolsuper", "rolbypassrls", "rolcreatedb", "rolcreaterole", "rolreplication",
			"cluster_membership", "object_creation", "ownership", "missing_capability", "excess_table", "excess_sequence", "excess_function", "missing_rls", "malformed_secdef",
		}).AddRow(
			posture.identity.roleOID, posture.identity.roleName, posture.identity.sessionRoleOID, posture.identity.sessionRoleName, posture.identity.systemIdentifier,
			posture.identity.databaseOID, posture.identity.databaseName, posture.identity.schemaOID,
			posture.identity.schemaName, posture.identity.authorityOwnerOID,
			posture.superuser, posture.bypassRLS, posture.createDatabase, posture.createRole, posture.replication,
			posture.setRoleMembership, posture.untrustedObjectCreation, posture.hasApplicationOwnership,
			posture.missingRequiredCapability, posture.hasExcessTableAuthority, posture.hasExcessSequenceAuthority, posture.hasExcessFunctionAuthority,
			posture.missingTenantRLSFence, posture.malformedSecurityDefinerFence,
		))
}

func expectMigrationIdentity(mock sqlmock.Sqlmock, identity providerControlDatabaseIdentity) {
	mock.ExpectQuery(regexp.QuoteMeta(providerControlMigrationRoleIdentityQuery)).
		WillReturnRows(sqlmock.NewRows([]string{
			"role_oid", "role_name", "session_role_oid", "session_role_name", "system_identifier", "database_oid", "database_name", "schema_oid", "schema_name", "authority_owner_oid",
		}).AddRow(
			identity.roleOID, identity.roleName, identity.sessionRoleOID, identity.sessionRoleName, identity.systemIdentifier, identity.databaseOID,
			identity.databaseName, identity.schemaOID, identity.schemaName, identity.authorityOwnerOID,
		))
}

func assertProviderDatabaseExpectations(t *testing.T, mocks ...sqlmock.Sqlmock) {
	t.Helper()
	for _, mock := range mocks {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("database expectations: %v", err)
		}
	}
}
