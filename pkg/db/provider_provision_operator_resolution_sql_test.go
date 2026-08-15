package db

import (
	"strings"
	"testing"
)

func TestProviderProvisionOperatorResolutionMigrationIsAppendOnlyAndFailClosed(t *testing.T) {
	content := readDBFile(t, "migrations/031_provider_provision_operator_resolution.sql")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS provider_provision_discovery_observations",
		"CREATE TABLE IF NOT EXISTS provider_provision_resolution_decisions",
		"observed_resolution_revision",
		"observation_snapshot_digest",
		"candidate_graphs_json",
		"jsonb_array_length(candidate_graphs_json) = candidate_count",
		"no_candidate_observed",
		"adopted_exact_candidate",
		"multiple_candidates_quarantined",
		"provider_provision_discovery_validate_insert",
		"provider_provision_resolution_validate_insert",
		"provider provision discovery and resolution rows are append-only",
		"FOR SHARE OF runtime_lease, runtime_server",
		"provision discovery cannot race a live dispatch claim",
		"AMO exact-candidate adoption requires its transaction-bound decision",
		"current_setting('app.provider_resolution_token', true)",
		"ALTER TABLE provider_provision_discovery_observations FORCE ROW LEVEL SECURITY",
		"ALTER TABLE provider_provision_resolution_decisions FORCE ROW LEVEL SECURITY",
		"ON DELETE RESTRICT",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("operator resolution migration missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"retry_count",
		"retry_at",
		"rearm",
		"DELETE FROM provider_provision_dispatch_guards",
		"UPDATE provider_provision_dispatch_guards",
		"legacy_simulate",
		"provisioning-executor/v1",
	} {
		if strings.Contains(content, forbidden) {
			t.Errorf("operator resolution migration introduced forbidden behavior via %q", forbidden)
		}
	}
}
