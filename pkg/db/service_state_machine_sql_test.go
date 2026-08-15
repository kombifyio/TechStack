package db

import (
	"strings"
	"testing"
)

const serviceStateMachineMigration = "migrations/073_service_state_machine.sql"

func TestServiceStateMachineMigrationPinsTheStackKitsVocabulary(t *testing.T) {
	content := readDBFile(t, serviceStateMachineMigration)
	for _, required := range []string{
		"ALTER TABLE services ADD COLUMN IF NOT EXISTS revision bigint NOT NULL DEFAULT 0",
		"CONSTRAINT services_revision_nonnegative CHECK (revision >= 0)",
		"ALTER TABLE services ADD CONSTRAINT services_desired_state_check\n    CHECK (desired_state IN ('running', 'stopped'))",
		"ALTER TABLE services ADD CONSTRAINT services_observed_state_check\n    CHECK (observed_state IN ('running', 'stopped', 'starting', 'error', 'unknown'))",
		"ALTER TABLE services ADD CONSTRAINT services_health_state_check\n    CHECK (health_state IN ('healthy', 'unhealthy', 'starting', 'not-required', 'unknown'))",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("migration is missing %q", required)
		}
	}
}

func TestServiceStateMachineMigrationNormalizesRowsBeforeConstraining(t *testing.T) {
	content := readDBFile(t, serviceStateMachineMigration)
	for _, dropped := range []string{
		"ALTER TABLE services DROP CONSTRAINT IF EXISTS services_desired_state_check",
		"ALTER TABLE services DROP CONSTRAINT IF EXISTS services_observed_state_check",
		"ALTER TABLE services DROP CONSTRAINT IF EXISTS services_health_state_check",
	} {
		if !strings.Contains(content, dropped) {
			t.Fatalf("migration is missing %q", dropped)
		}
	}
	for _, dimension := range []string{"desired_state", "observed_state", "health_state"} {
		backfill := strings.Index(content, "UPDATE services\nSET "+dimension+" = CASE")
		constrain := strings.Index(content, "ALTER TABLE services ADD CONSTRAINT services_"+
			strings.TrimSuffix(dimension, "_state")+"_state_check")
		drop := strings.Index(content, "ALTER TABLE services DROP CONSTRAINT IF EXISTS services_"+
			strings.TrimSuffix(dimension, "_state")+"_state_check")
		if drop < 0 || backfill < 0 || constrain < 0 {
			t.Fatalf("%s migration steps are missing", dimension)
		}
		if drop > backfill || backfill > constrain {
			t.Fatalf("%s must be dropped, backfilled, and only then constrained", dimension)
		}
	}
	// `reachable` is not part of the canonical health vocabulary and must be
	// mapped to the unproven-but-live value, never to healthy.
	starting := strings.Index(content, "'starting', 'reachable', 'created', 'restarting', 'pending', 'initializing', 'deploying'\n    ) THEN 'starting'")
	if starting < 0 {
		t.Fatal("reachable health evidence must migrate to starting")
	}
}

func TestServiceStateMachineMigrationSeparatesStatusFromHealth(t *testing.T) {
	content := readDBFile(t, serviceStateMachineMigration)
	repair := strings.Index(content, "UPDATE services\nSET status = observed_state")
	if repair < 0 {
		t.Fatal("migration does not repair the historical status/health conflation")
	}
	for _, required := range []string{
		"lower(btrim(COALESCE(migration_status, ''))) NOT IN (\n        'migrating', 'deploying', 'pending_verification', 'archived'\n    )",
		"'healthy', 'unhealthy', 'reachable', 'not-required', 'not_required'",
	} {
		if !strings.Contains(content[repair:], required) {
			t.Fatalf("status repair is missing %q", required)
		}
	}
}

func TestServiceStateTransitionsMirrorTheServerTimeline(t *testing.T) {
	content := readDBFile(t, serviceStateMachineMigration)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS service_state_transitions",
		"id bigserial PRIMARY KEY",
		"tenant_id text NOT NULL",
		"CHECK (btrim(tenant_id) <> '')",
		"service_id text NOT NULL",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_services_tenant_identity\n    ON services (tenant_id, id)",
		"FOREIGN KEY (tenant_id, service_id) REFERENCES services (tenant_id, id) ON DELETE CASCADE",
		"dimension text NOT NULL",
		"from_state text",
		"to_state text NOT NULL",
		"reason_code text",
		"source text NOT NULL",
		"observed_at timestamptz NOT NULL",
		"evidence_json jsonb NOT NULL DEFAULT '{}'::jsonb",
		"created_at timestamptz NOT NULL DEFAULT now()",
		"CHECK (dimension IN ('desired', 'observed', 'health'))",
		"CHECK (from_state IS DISTINCT FROM to_state)",
		"CHECK (reason_code IS NULL OR (btrim(reason_code) <> '' AND octet_length(reason_code) <= 128))",
		"CREATE INDEX IF NOT EXISTS idx_service_state_transitions_timeline\n    ON service_state_transitions (tenant_id, service_id, id DESC)",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("transition table is missing %q", required)
		}
	}
}

func TestServiceStateTransitionsKeepTheServerRLSPosture(t *testing.T) {
	content := readDBFile(t, serviceStateMachineMigration)
	enable := strings.Index(content, "ALTER TABLE service_state_transitions ENABLE ROW LEVEL SECURITY")
	force := strings.Index(content, "ALTER TABLE service_state_transitions FORCE ROW LEVEL SECURITY")
	drop := strings.Index(content, "DROP POLICY IF EXISTS tenant_isolation ON service_state_transitions")
	policy := strings.Index(content, "CREATE POLICY tenant_isolation ON service_state_transitions")
	if enable < 0 || force < 0 || drop < 0 || policy < 0 {
		t.Fatal("service_state_transitions must be tenant isolated with forced RLS")
	}
	if enable > force || force > drop || drop > policy {
		t.Fatal("RLS must be enabled and forced before the tenant policy is created")
	}
	for _, required := range []string{
		"USING (tenant_id = current_setting('app.tenant_id', true))",
		"WITH CHECK (tenant_id = current_setting('app.tenant_id', true))",
		"REVOKE ALL ON TABLE service_state_transitions FROM PUBLIC",
	} {
		if !strings.Contains(content[policy:], required) && !strings.Contains(content, required) {
			t.Fatalf("tenant policy is missing %q", required)
		}
	}
}

func TestServiceStateMachineMigrationNumberIsUnclaimed(t *testing.T) {
	// 072 is claimed twice already (RIL wake scope and the registry observation
	// sweep), so the service state machine takes the next free number.
	for _, taken := range []string{
		"migrations/072_ril_signal_delivery_wake_schema_scope.sql",
		"migrations/072_server_registry_observation_sweep.sql",
	} {
		if content := readDBFile(t, taken); strings.Contains(content, "service_state_transitions") {
			t.Fatalf("%s already defines the service state machine", taken)
		}
	}
}
