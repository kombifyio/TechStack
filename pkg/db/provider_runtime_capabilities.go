package db

// ProviderRuntimeTableGrants is the existing restricted provider capability
// authority, shared by provider installation and managed application audit.
type ProviderRuntimeTableGrant struct{ Table, Privileges string }

var ProviderRuntimeTableGrants = []ProviderRuntimeTableGrant{
	{Table: "provider_catalog_versions", Privileges: "SELECT"},
	{Table: "provider_catalog_profiles", Privileges: "SELECT"},
	{Table: "provider_credential_handles", Privileges: "SELECT,INSERT"},
	{Table: "substrate_bindings", Privileges: "SELECT"},
	{Table: "substrate_guest_leases", Privileges: "SELECT,INSERT"},
	{Table: "home_assistant_owner_bindings", Privileges: "SELECT,INSERT,UPDATE"},
	{Table: "typed_agent_commands", Privileges: "SELECT,INSERT,UPDATE"},
	{Table: "servers", Privileges: "SELECT,INSERT,UPDATE"},
	{Table: "server_state_transitions", Privileges: "SELECT,INSERT"},
	{Table: "server_registry_outbox", Privileges: "SELECT,INSERT"},
	{Table: "techstack_vm_leases", Privileges: "SELECT,INSERT,UPDATE"},
	{Table: "runtime_lease_execution_authorities", Privileges: "SELECT,INSERT"},
	{Table: "runtime_lease_idempotency_records", Privileges: "SELECT,INSERT"},
	{Table: "managed_runtime_capacity_reservations", Privileges: "SELECT,INSERT"},
	{Table: "managed_runtime_server_slots", Privileges: "SELECT,INSERT"},
	{Table: "managed_runtime_server_slot_generations", Privileges: "SELECT,INSERT"},
	{Table: "managed_runtime_capacity_release_facts", Privileges: "SELECT,INSERT"},
	{Table: "managed_runtime_capacity_quarantine_retirements", Privileges: "SELECT"},
	{Table: "provider_desired_spec_revisions", Privileges: "SELECT,INSERT,UPDATE"},
	{Table: "provider_operations", Privileges: "SELECT,INSERT,UPDATE"},
	{Table: "provider_async_requests", Privileges: "SELECT,INSERT,UPDATE"},
	{Table: "provider_operation_receipts", Privileges: "SELECT,INSERT"},
	{Table: "provider_operation_resources", Privileges: "SELECT,INSERT,UPDATE"},
	{Table: "provider_operation_evidence", Privileges: "SELECT,INSERT,UPDATE"},
	// Absence observations are append-only custody: the adapter records the
	// provider read, the verifier reads it back. No UPDATE, no DELETE.
	{Table: "provider_absence_observations", Privileges: "SELECT,INSERT"},
	{Table: "provider_operation_execution_claims", Privileges: "SELECT,INSERT,UPDATE"},
	{Table: "provider_provision_dispatch_guards", Privileges: "SELECT,INSERT"},
	// Provision resolution is append-only operator custody. Recovery records the
	// certified provider read and its decision through the runtime pool, then
	// reads both back to settle the exact generation. No UPDATE, no DELETE.
	{Table: "provider_provision_discovery_observations", Privileges: "SELECT,INSERT"},
	{Table: "provider_provision_resolution_decisions", Privileges: "SELECT,INSERT"},
	{Table: "server_provider_resource_bindings", Privileges: "SELECT,INSERT"},
	{Table: "provider_operation_resource_free_terminalizations", Privileges: "SELECT,INSERT"},
	// Managed provisioning records the node's Guard enrolment capability on the
	// dispatch path so the VM can redeem it on first boot. INSERT only: the
	// runtime never reads a capability back, and redemption is a separate
	// authenticated surface.
	{Table: "pairing_tokens", Privileges: "SELECT,INSERT"},
	// Terminal observation only upserts/reads incidents and enqueues an
	// idempotent workflow run. Advisory advancement remains control-plane-only.
	{Table: "provider_incidents", Privileges: "SELECT,INSERT"},
	// ON CONFLICT DO NOTHING requires SELECT on the conflict key for the
	// idempotency check; no workflow state mutation is granted.
	{Table: "ril_workflow_runs", Privileges: "SELECT,INSERT"},
}
