package db

import (
	"strings"
	"testing"
)

func TestProviderCatalogMigrationIsVersionedTenantScopedAndSecretFree(t *testing.T) {
	content := readDBFile(t, "migrations/027_provider_catalog_authority.sql")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS provider_catalog_versions",
		"CREATE UNIQUE INDEX IF NOT EXISTS uq_provider_catalog_one_active_version",
		"WHERE status = 'active'",
		"CREATE TABLE IF NOT EXISTS provider_catalog_profiles",
		"provider_id text NOT NULL",
		"adapter_id text NOT NULL",
		"credential_mode text NOT NULL",
		"runtime_profile_id text NOT NULL",
		"offering_id text NOT NULL",
		"can_pause boolean NOT NULL",
		"stop_effect text NOT NULL",
		"can_recreate boolean NOT NULL",
		"capability_snapshot jsonb NOT NULL",
		"CREATE OR REPLACE FUNCTION provider_catalog_capability_json_within_limits",
		"octet_length(snapshot::text) <= 16384",
		"(SELECT count(*) FROM nodes) <= 256",
		"(SELECT max(depth) FROM nodes) <= 4",
		"length(node_value #>> '{}') NOT BETWEEN 1 AND 128",
		"CREATE OR REPLACE FUNCTION provider_catalog_capability_string_array_is_valid",
		"jsonb_array_length(value) > 32",
		"CREATE OR REPLACE FUNCTION provider_catalog_capability_snapshot_is_valid",
		"'regions', 'zones', 'instance_types', 'architectures', 'network', 'storage'",
		"'ipv4', 'ipv6', 'private_network', 'floating_ip'",
		"'volume_types', 'snapshots', 'online_resize'",
		"provider_catalog_capability_json_within_limits(capability_snapshot)",
		"provider_catalog_capability_snapshot_is_valid(capability_snapshot)",
		"catalog_version ~ '^[a-z0-9][a-z0-9._-]{0,127}$'",
		"provider_id ~ '^[a-z0-9][a-z0-9._-]{0,127}$'",
		"adapter_id ~ '^[a-z0-9][a-z0-9._-]{0,127}$'",
		"runtime_profile_id ~ '^[a-z0-9][a-z0-9._-]{0,127}$'",
		"offering_id ~ '^[a-z0-9][a-z0-9._-]{0,127}$'",
		"provider_id NOT IN ('centron-managed', 'ionos-managed')",
		"CREATE TABLE IF NOT EXISTS provider_credential_handles",
		"PRIMARY KEY (tenant_id, handle_id, handle_version)",
		"UNIQUE (tenant_id, custody_ref)",
		"custody_ref ~ '^custody://[A-Za-z0-9][A-Za-z0-9._~/-]*$'",
		"connection_ref ~ '^provider-connection://[A-Za-z0-9][A-Za-z0-9._~/-]*$'",
		"subject_kind text NOT NULL",
		"subject_id text NOT NULL",
		"grant_id text NOT NULL",
		"credential_scope text NOT NULL",
		"revoked_at timestamptz",
		"provider catalog profiles may be added only to a draft version",
		"pg_catalog.quote_ident(pg_catalog.current_schema()) || ', pg_catalog, pg_temp'",
		"SET search_path FROM CURRENT",
		"FROM provider_catalog_versions",
		"provider_catalog_capability_string_array_is_valid(",
		"WHERE catalog_version = NEW.catalog_version\n    FOR UPDATE",
		"provider catalog activation time is immutable",
		"credential handle identity is immutable and revocation is irreversible",
		"ENABLE ROW LEVEL SECURITY",
		"FORCE ROW LEVEL SECURITY",
		"current_setting('app.tenant_id', true)",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("provider catalog migration missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"secret_json", "credential_json", "credential_value", "api_token", "private_key", "password text",
		"insert into provider_catalog_versions", "insert into provider_catalog_profiles", "insert into provider_credential_handles",
		"set local search_path = public", "public.provider_catalog_versions",
		"public.provider_catalog_capability_string_array_is_valid",
	} {
		if strings.Contains(strings.ToLower(content), forbidden) {
			t.Errorf("provider catalog migration must not persist %q", forbidden)
		}
	}
	if count := strings.Count(content, "provider_id NOT IN ('centron-managed', 'ionos-managed')"); count != 2 {
		t.Errorf("legacy provider aliases must be rejected by profile and handle persistence; checks = %d, want 2", count)
	}
	functionCount := strings.Count(content, "CREATE OR REPLACE FUNCTION ")
	if got := strings.Count(content, "SET search_path FROM CURRENT"); got != functionCount {
		t.Fatalf("hardened catalog function search paths = %d, want %d", got, functionCount)
	}
}
