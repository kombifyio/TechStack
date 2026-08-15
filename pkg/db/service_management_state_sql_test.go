package db

import (
	"strings"
	"testing"
)

const serviceManagementStateMigration = "migrations/074_service_management_state.sql"

func TestServiceManagementStateMigrationNumberIsUnclaimed(t *testing.T) {
	// 072 is claimed twice (RIL wake scope and the registry observation sweep)
	// and 073 is the service state machine, so the management dimension takes
	// the next free number.
	for _, taken := range []string{
		"migrations/072_ril_signal_delivery_wake_schema_scope.sql",
		"migrations/072_server_registry_observation_sweep.sql",
		serviceStateMachineMigration,
	} {
		if content := readDBFile(t, taken); strings.Contains(content, "management_state") {
			t.Fatalf("%s already defines the service management dimension", taken)
		}
	}
}

func TestServiceManagementStateMigrationPinsTheOwnershipVocabulary(t *testing.T) {
	content := readDBFile(t, serviceManagementStateMigration)
	for _, required := range []string{
		"ALTER TABLE services ADD COLUMN IF NOT EXISTS management_state text NOT NULL DEFAULT 'observed'",
		"ALTER TABLE services ADD CONSTRAINT services_management_state_check\n    CHECK (management_state IN ('managed', 'observed'))",
		"ALTER TABLE services ADD CONSTRAINT services_source_check\n    CHECK (source IN ('observed', 'stackkits-inventory', 'stackkit_outputs', 'legacy-registry-backfill'))",
		"CHECK (dimension IN ('desired', 'observed', 'health', 'management'))",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("migration is missing %q", required)
		}
	}
}

// The historical read-time derivation had three inputs. Losing any of them
// would silently change what an existing row means.
func TestServiceManagementStateBackfillReproducesTheHistoricalDerivation(t *testing.T) {
	content := readDBFile(t, serviceManagementStateMigration)
	backfill := strings.Index(content, "SET management_state = CASE")
	if backfill < 0 {
		t.Fatal("migration does not backfill management_state")
	}
	for _, marker := range []string{
		"WHEN lower(btrim(COALESCE(source, ''))) = 'observed' THEN 'observed'",
		"WHEN lower(btrim(COALESCE(status, ''))) = 'observed' THEN 'observed'",
		"WHEN lower(btrim(COALESCE(metadata_json->>'type', ''))) = 'custom' THEN 'observed'",
		"ELSE 'managed'",
	} {
		if !strings.Contains(content[backfill:], marker) {
			t.Fatalf("backfill is missing the %q rule", marker)
		}
	}
}

// Ownership must be computed from the pre-normalization provenance: folding an
// unrecognized source to 'observed' first would flip such a row from managed to
// observed.
func TestServiceManagementStateBackfillRunsBeforeTheSourceFold(t *testing.T) {
	content := readDBFile(t, serviceManagementStateMigration)
	ownership := strings.Index(content, "SET management_state = CASE")
	fold := strings.Index(content, "SET source = CASE")
	constrainSource := strings.Index(content, "ALTER TABLE services ADD CONSTRAINT services_source_check")
	dropSource := strings.Index(content, "ALTER TABLE services DROP CONSTRAINT IF EXISTS services_source_check")
	if ownership < 0 || fold < 0 || constrainSource < 0 || dropSource < 0 {
		t.Fatal("management/source migration steps are missing")
	}
	if ownership > fold {
		t.Fatal("management_state must be derived before source is normalized")
	}
	if dropSource > fold || fold > constrainSource {
		t.Fatal("source must be dropped, normalized, and only then constrained")
	}
}
