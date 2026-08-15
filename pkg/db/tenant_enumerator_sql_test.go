package db

import (
	"strings"
	"testing"
)

func TestProviderIncidentTenantEnumeratorMigrationIsLeastPrivilege(t *testing.T) {
	content, err := migrationsFS.ReadFile("migrations/070_provider_incident_tenant_enumerator.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(content)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS provider_incident_pending_dispatch_tenants",
		"CREATE OR REPLACE FUNCTION provider_incident_refresh_pending_dispatch_tenant()",
		"RETURNS TABLE (tenant_id text)",
		"SECURITY DEFINER",
		"ALTER FUNCTION %I.%I() OWNER TO %I",
		"SET search_path TO pg_catalog, %I, pg_temp",
		"REVOKE ALL ON TABLE provider_incident_pending_dispatch_tenants FROM PUBLIC",
		"REVOKE ALL ON FUNCTION %I.%I() FROM PUBLIC",
		"GRANT EXECUTE ON FUNCTION %I.provider_incident_list_tenant_ids() TO %I",
		"ALTER TABLE provider_incidents NO FORCE ROW LEVEL SECURITY",
		"ALTER TABLE provider_incidents FORCE ROW LEVEL SECURITY",
		"FROM provider_incident_pending_dispatch_tenants AS directory",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("migration missing posture invariant %q", required)
		}
	}
	if strings.Contains(source, "SELECT id\n    FROM techstack_tenants") {
		t.Fatal("incident tenant enumerator still depends on tenant table RLS")
	}
	if strings.Contains(source, "techstack_provider_control_runtime") {
		t.Fatal("incident enumerator must not grant the provider runtime role")
	}
}
