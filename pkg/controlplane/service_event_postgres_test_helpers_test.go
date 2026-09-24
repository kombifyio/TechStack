package controlplane

import "github.com/DATA-DOG/go-sqlmock"

func serviceAggregateHeadPattern() string {
	return `(?s)SELECT .*FROM services\s+WHERE tenant_id = \$1 AND id = \$2\s+FOR UPDATE`
}

func serviceAggregateInsertPattern() string {
	return `(?s)INSERT INTO services \(.*ON CONFLICT \(id\) DO NOTHING.*RETURNING`
}

func serviceAggregateUpdatePattern() string {
	return `(?s)UPDATE services SET.*revision = \$30, management_state = \$31.*WHERE tenant_id = \$2 AND id = \$1 AND revision = \$36.*RETURNING`
}

func serviceAggregateRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "instance_id", "stack_id", "server_id",
		"target_kind", "provider_id", "managed_target_ref", "provider_receipt_ref",
		"sla_policy_ref", "backup_policy_ref", "placement_evidence_ref", "placement_observed_at",
		"service_key", "service_instance", "name", "desired_state", "observed_state", "health_state",
		"management_state", "mutation_lock_state", "mutation_lock_reason_code",
		"mutation_lock_actor", "mutation_lock_changed_at",
		"observed_at", "stackkit_version", "access_json", "capabilities_json",
		"source", "metadata_json", "created_at", "updated_at",
		"revision", "status", "migration_status", "node_id", "url",
	})
}

func serviceTransitionRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "service_id", "dimension", "from_state", "to_state",
		"reason_code", "source", "observed_at", "evidence_json", "created_at",
	})
}
