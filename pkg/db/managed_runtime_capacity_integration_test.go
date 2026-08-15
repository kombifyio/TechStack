package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const managedRuntimeCapacityMigration = "migrations/033_managed_runtime_capacity_reservations.sql"

func TestIntegrationManagedRuntimeCapacityQuarantinesCanonicalLegacyAlias(t *testing.T) {
	database := openManagedRuntimeCapacityPremigrationDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	applyManagedRuntimeCapacityMigrationsBefore033(t, ctx, database)

	generationID := uuid.NewString()
	seedManagedRuntimeCapacityHistoricalLease(t, ctx, database, historicalCapacityLease{
		TenantID: "tenant-capacity-known", OwnerID: "owner-capacity-known",
		LeaseID: "lease-capacity-known", ServerID: "server-capacity-known",
		GenerationID: generationID, ResourceProviderID: "ionos-managed",
	})
	if err := applyManagedRuntimeCapacityMigration033(ctx, database); err != nil {
		t.Fatalf("apply migration 033 over canonical legacy alias: %v", err)
	}

	tx, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("begin quarantine assertion: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, "tenant-capacity-known"); err != nil {
		t.Fatalf("set quarantine assertion tenant: %v", err)
	}
	var ownerID, providerID, storedGeneration, mode, origin, policySource, policyDigest string
	var operationID sql.NullString
	var capacityLimit sql.NullInt64
	if err := tx.QueryRowContext(ctx, `
		SELECT owner_subject_id, provider_id, resource_generation_id::text, operation_id,
		       reservation_mode, capacity_limit, policy_source, policy_digest,
		       reservation_origin
		FROM managed_runtime_capacity_reservations
		WHERE tenant_id = $1 AND lease_id = $2
	`, "tenant-capacity-known", "lease-capacity-known").Scan(
		&ownerID, &providerID, &storedGeneration, &operationID, &mode, &capacityLimit,
		&policySource, &policyDigest, &origin,
	); err != nil {
		t.Fatalf("load historical capacity quarantine: %v", err)
	}
	if ownerID != "owner-capacity-known" || providerID != "ionos" ||
		storedGeneration != generationID || operationID.Valid ||
		mode != "quarantine" || capacityLimit.Valid || origin != "migration_quarantine" ||
		policySource != "migration_quarantine:managed-runtime-capacity/v1" ||
		!strings.HasPrefix(policyDigest, "sha256:") {
		t.Fatalf(
			"historical capacity quarantine = owner %q provider %q generation %q operation %v mode %q limit %v source %q policy %q origin %q",
			ownerID, providerID, storedGeneration, operationID, mode, capacityLimit,
			policySource, policyDigest, origin,
		)
	}
}

func TestIntegrationManagedRuntimeCapacityRejectsUnknownProviderHandleWithoutBillingMetadata(t *testing.T) {
	database := openManagedRuntimeCapacityPremigrationDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	applyManagedRuntimeCapacityMigrationsBefore033(t, ctx, database)

	generationID := uuid.NewString()
	seedManagedRuntimeCapacityHistoricalLease(t, ctx, database, historicalCapacityLease{
		TenantID: "tenant-capacity-handle", OwnerID: "owner-capacity-handle",
		LeaseID: "lease-capacity-handle", ServerID: "server-capacity-handle",
		GenerationID: generationID, TypedProviderID: "unregistered-cloud",
		ResourceProviderID: "unregistered-cloud",
	})
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin unknown-handle fixture: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, "tenant-capacity-handle"); err != nil {
		t.Fatalf("set unknown-handle tenant: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE techstack_vm_leases
		SET engine_vm_id = 'external-vm-without-billing-metadata',
		    lease_json = jsonb_build_object(
		        'metadata', jsonb_build_object('resource_generation_id', $3::text)
		    )
		WHERE tenant_id = $1 AND id = $2
	`, "tenant-capacity-handle", "lease-capacity-handle", generationID); err != nil {
		t.Fatalf("remove optional billing metadata and retain provider handle: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit unknown-handle fixture: %v", err)
	}

	err = applyManagedRuntimeCapacityMigration033(ctx, database)
	if err == nil || !strings.Contains(err.Error(),
		"externally cost-bearing custody without exact IONOS or Centron identity") {
		t.Fatalf("unknown provider handle migration error = %v, want fail-closed custody rejection", err)
	}
}

func TestIntegrationManagedRuntimeCapacityRejectsUnknownCanonicalProvider(t *testing.T) {
	database := openManagedRuntimeCapacityPremigrationDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	applyManagedRuntimeCapacityMigrationsBefore033(t, ctx, database)

	seedManagedRuntimeCapacityHistoricalLease(t, ctx, database, historicalCapacityLease{
		TenantID: "tenant-capacity-unknown", OwnerID: "owner-capacity-unknown",
		LeaseID: "lease-capacity-unknown", ServerID: "server-capacity-unknown",
		GenerationID: uuid.NewString(), ResourceProviderID: "unregistered-cloud",
	})
	err := applyManagedRuntimeCapacityMigration033(ctx, database)
	if err == nil || !strings.Contains(err.Error(),
		"externally cost-bearing custody without exact IONOS or Centron identity") {
		t.Fatalf("unknown canonical provider migration error = %v, want fail-closed custody rejection", err)
	}
}

func TestIntegrationManagedRuntimeCapacityRejectsConflictingProviderCustody(t *testing.T) {
	database := openManagedRuntimeCapacityPremigrationDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	applyManagedRuntimeCapacityMigrationsBefore033(t, ctx, database)

	seedManagedRuntimeCapacityHistoricalLease(t, ctx, database, historicalCapacityLease{
		TenantID: "tenant-capacity-conflict", OwnerID: "owner-capacity-conflict",
		LeaseID: "lease-capacity-conflict", ServerID: "server-capacity-conflict",
		GenerationID: uuid.NewString(), TypedProviderID: "ionos", ResourceProviderID: "centron",
	})
	err := applyManagedRuntimeCapacityMigration033(ctx, database)
	if err == nil || !strings.Contains(err.Error(),
		"conflicting IONOS and Centron custody identity") {
		t.Fatalf("conflicting provider migration error = %v, want fail-closed custody rejection", err)
	}
}

func TestIntegrationManagedRuntimeCapacityCutoverRejectsActiveClaim(t *testing.T) {
	database := openManagedRuntimeCapacityPremigrationDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	applyManagedRuntimeCapacityMigrationsBefore(t, ctx, database, "029_")

	lease := historicalCapacityLease{
		TenantID: "tenant-capacity-active-claim", OwnerID: "owner-capacity-active-claim",
		LeaseID: "lease-capacity-active-claim", ServerID: "server-capacity-active-claim",
		GenerationID: uuid.NewString(), TypedProviderID: "ionos", ResourceProviderID: "ionos",
	}
	seedManagedRuntimeCapacityHistoricalLease(t, ctx, database, lease)
	seedManagedRuntimeCapacityPreDispatchActiveClaim(t, ctx, database, lease)
	for _, migration := range []string{
		"029_provider_provision_dispatch_guards.sql",
		"030_ril_action_execution_ledger.sql",
		"031_provider_provision_operator_resolution.sql",
		"032_provider_claim_credential_authority.sql",
	} {
		applyManagedRuntimeCapacityMigrationFile(t, ctx, database, "migrations/"+migration)
	}

	err := applyManagedRuntimeCapacityMigration033(ctx, database)
	if err == nil || !strings.Contains(err.Error(),
		"managed runtime capacity cutover requires all provider execution claims to be drained") {
		t.Fatalf("active claim migration error = %v, want stopped-lane drain rejection", err)
	}
}

func seedManagedRuntimeCapacityPreDispatchActiveClaim(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	lease historicalCapacityLease,
) {
	t.Helper()
	const (
		manifestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		commandDigest  = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		requested      = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		accepted       = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
		specDigest     = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
		capabilityHash = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
		profileHash    = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
		claimToken     = "capacity-cutover-token"
		claimOwner     = "capacity-cutover-worker"
	)
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin pre-dispatch active claim seed: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, lease.TenantID); err != nil {
		t.Fatalf("set pre-dispatch claim tenant: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		SELECT
			set_config('app.provider_execution_claim_token', $1, true),
			set_config('app.provider_execution_claim_owner', $2, true)
	`, claimToken, claimOwner); err != nil {
		t.Fatalf("bind pre-dispatch claim capability: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO runtime_lease_execution_authorities (
			tenant_id, lease_id, execution_authority, bound_at
		) VALUES ($1, $2, 'techstack_provider_control', clock_timestamp())
	`, lease.TenantID, lease.LeaseID); err != nil {
		t.Fatalf("seed pre-dispatch native authority: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO provider_catalog_versions (catalog_version, status)
		VALUES ('catalog-capacity-cutover', 'draft')
	`); err != nil {
		t.Fatalf("seed pre-dispatch catalog version: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO provider_catalog_profiles (
			catalog_version, provider_id, adapter_id, credential_mode,
			runtime_profile_id, offering_id, can_pause, stop_effect,
			can_recreate, capability_snapshot
		) VALUES (
			'catalog-capacity-cutover', 'ionos', 'ionos-v1', 'managed',
			'ionos-managed-pvm-monthly', 'monthly-runtime-standard',
			true, 'pause', true, '{}'::jsonb
		)
	`); err != nil {
		t.Fatalf("seed pre-dispatch execution profile: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO provider_desired_spec_revisions (
			tenant_id, lease_id, revision, spec_ref, spec_digest, spec_json, created_at
		) VALUES ($1, $2, 1, $3, $4, '{}'::jsonb, clock_timestamp())
	`, lease.TenantID, lease.LeaseID,
		"desired-spec://capacity-cutover/"+lease.LeaseID+"/revision-1", specDigest); err != nil {
		t.Fatalf("seed pre-dispatch desired spec: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO provider_operations (
			tenant_id, operation_id, lease_id, operation, idempotency_key,
			adapter_id, command_digest, command_json, ledger_revision,
			desired_spec_revision, status, phase, head_sequence,
			head_receipt_digest, requested_at, created_at, updated_at
		) VALUES (
			$1, 'operation-capacity-claim', $2, 'provision',
			'capacity-cutover-idempotency', 'ionos-v1', $3,
			jsonb_build_object(
				'schema_version', 'techstack.provider-control-operation/v1',
				'execution_authority', 'techstack_provider_control',
				'execution_profile', jsonb_build_object(
					'catalog_version', 'catalog-capacity-cutover',
					'provider_id', 'ionos',
					'adapter_id', 'ionos-v1',
					'credential_mode', 'managed',
					'runtime_profile_id', 'ionos-managed-pvm-monthly',
					'offering_id', 'monthly-runtime-standard',
					'adapter_manifest_hash', $4::text,
					'capability_snapshot_hash', $8::text,
					'execution_profile_hash', $9::text
				),
				'command', jsonb_build_object(
					'provider_id', 'ionos',
					'lease_revision', 1,
					'runtime_server_id', $5::text,
					'resource_generation_id', $6::text
				)
			),
			1, 1, 'pending', 'requested', 1, $7,
			clock_timestamp(), clock_timestamp(), clock_timestamp()
		)
	`, lease.TenantID, lease.LeaseID, commandDigest, manifestDigest,
		lease.ServerID, lease.GenerationID, requested, capabilityHash, profileHash); err != nil {
		t.Fatalf("seed pre-dispatch provision operation: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO provider_operation_receipts (
			tenant_id, operation_id, sequence, previous_receipt_digest,
			receipt_digest, status, phase, receipt_json, issued_at
		) VALUES
			($1, 'operation-capacity-claim', 1, NULL, $2,
			 'pending', 'requested', '{}'::jsonb, clock_timestamp()),
			($1, 'operation-capacity-claim', 2, $2, $3,
			 'pending', 'accepted', '{}'::jsonb, clock_timestamp())
	`, lease.TenantID, requested, accepted); err != nil {
		t.Fatalf("seed pre-dispatch provision receipts: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE provider_operations
		SET phase = 'accepted', head_sequence = 2,
			head_receipt_digest = $3, updated_at = clock_timestamp()
		WHERE tenant_id = $1 AND operation_id = 'operation-capacity-claim'
		  AND lease_id = $2
	`, lease.TenantID, lease.LeaseID, accepted); err != nil {
		t.Fatalf("advance pre-dispatch provision head: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO provider_operation_execution_claims (
			tenant_id, operation_id, head_sequence, head_receipt_digest,
			claim_token_digest, claim_owner, state, claimed_at, lease_expires_at
		) VALUES (
			$1, 'operation-capacity-claim', 2, $2,
			encode(sha256(convert_to($3, 'UTF8')), 'hex'), $4, 'active',
			clock_timestamp(), clock_timestamp() + interval '1 minute'
		)
	`, lease.TenantID, accepted, claimToken, claimOwner); err != nil {
		t.Fatalf("seed pre-dispatch active provider claim: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit pre-dispatch active provider claim: %v", err)
	}
}

func TestIntegrationManagedRuntimeCapacityQuarantineCannotAuthorizeProvisionClaim(t *testing.T) {
	database := openManagedRuntimeCapacityPremigrationDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	applyManagedRuntimeCapacityMigrationsBefore033(t, ctx, database)

	generationID := uuid.NewString()
	lease := historicalCapacityLease{
		TenantID: "tenant-capacity-claim", OwnerID: "owner-capacity-claim",
		LeaseID: "lease-capacity-claim", ServerID: "server-capacity-claim",
		GenerationID: generationID, TypedProviderID: "ionos", ResourceProviderID: "ionos",
	}
	seedManagedRuntimeCapacityHistoricalLease(t, ctx, database, lease)
	receiptDigest := seedManagedRuntimeCapacityHistoricalNativeOperation(t, ctx, database, lease)
	if err := applyManagedRuntimeCapacityMigration033(ctx, database); err != nil {
		t.Fatalf("apply migration 033 over historical native operation: %v", err)
	}

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin quarantined claim attempt: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, lease.TenantID); err != nil {
		t.Fatalf("set quarantined claim tenant: %v", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO provider_operation_execution_claims (
			tenant_id, operation_id, head_sequence, head_receipt_digest,
			claim_token_digest, claim_owner, claim_access, state,
			claimed_at, lease_expires_at
		) VALUES (
			$1, 'operation-capacity-claim', 1, $2,
			$3, 'capacity-claim-worker', 'side_effecting', 'active',
			clock_timestamp(), clock_timestamp() + interval '1 minute'
		)
	`, lease.TenantID, receiptDigest, strings.Repeat("a", 64))
	if err == nil || !strings.Contains(err.Error(),
		"provider execution claim is blocked by missing native managed runtime capacity authority") {
		t.Fatalf("quarantined provision claim error = %v, want native capacity authority rejection", err)
	}
}

func TestIntegrationManagedRuntimeCapacityRejectsPreCapacityNativeWriter(t *testing.T) {
	database := openManagedRuntimeCapacityPremigrationDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	applyManagedRuntimeCapacityMigrationsBefore033(t, ctx, database)

	generationID := uuid.NewString()
	seedManagedRuntimeCapacityHistoricalLease(t, ctx, database, historicalCapacityLease{
		TenantID: "tenant-capacity-writer", OwnerID: "owner-capacity-writer",
		LeaseID: "lease-capacity-writer", ServerID: "server-capacity-writer",
		GenerationID: generationID, TypedProviderID: "ionos", ResourceProviderID: "ionos",
	})
	if err := applyManagedRuntimeCapacityMigration033(ctx, database); err != nil {
		t.Fatalf("apply migration 033 before old-writer check: %v", err)
	}

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin pre-capacity writer transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, "tenant-capacity-writer"); err != nil {
		t.Fatalf("set old-writer tenant: %v", err)
	}
	const manifestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO provider_catalog_versions (catalog_version, status)
		VALUES ('catalog-capacity-writer', 'draft')
	`); err != nil {
		t.Fatalf("seed old-writer catalog version: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO provider_catalog_profiles (
			catalog_version, provider_id, adapter_id, credential_mode,
			runtime_profile_id, offering_id, can_pause, stop_effect,
			can_recreate, capability_snapshot, adapter_manifest_hash,
			provision_dispatch_mode
		) VALUES (
			'catalog-capacity-writer', 'ionos', 'ionos-v1', 'managed',
			'ionos-managed-pvm-monthly', 'monthly-runtime-standard',
			true, 'pause', true, '{}'::jsonb, $1, 'native_idempotency'
		)
	`, manifestDigest); err != nil {
		t.Fatalf("seed old-writer execution profile: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO provider_desired_spec_revisions (
			tenant_id, lease_id, revision, spec_ref, spec_digest, spec_json, created_at
		) VALUES (
			'tenant-capacity-writer', 'lease-capacity-writer', 1,
			'desired-spec://capacity-writer/revision-1',
			'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			'{}'::jsonb, clock_timestamp()
		)
	`); err != nil {
		t.Fatalf("seed old-writer desired spec: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO provider_operations (
			tenant_id, operation_id, lease_id, operation, idempotency_key,
			adapter_id, command_digest, command_json, ledger_revision,
			desired_spec_revision, status, phase, head_sequence,
			head_receipt_digest, requested_at, created_at, updated_at,
			provision_dispatch_mode
		) VALUES (
			'tenant-capacity-writer', 'operation-capacity-writer',
			'lease-capacity-writer', 'provision', 'capacity-writer-idempotency',
			'ionos-v1',
			'sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',
			jsonb_build_object(
				'schema_version', 'techstack.provider-control-operation/v1',
				'execution_authority', 'techstack_provider_control',
				'execution_profile', jsonb_build_object(
					'catalog_version', 'catalog-capacity-writer',
					'provider_id', 'ionos',
					'adapter_id', 'ionos-v1',
					'credential_mode', 'managed',
					'runtime_profile_id', 'ionos-managed-pvm-monthly',
					'offering_id', 'monthly-runtime-standard',
					'provision_dispatch_mode', 'native_idempotency',
					'adapter_manifest_hash', $1::text
				),
				'command', jsonb_build_object(
					'resource_generation_id', $2::text
				)
			),
			1, 1, 'pending', 'requested', 1,
			'sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd',
			clock_timestamp(), clock_timestamp(), clock_timestamp(),
			'native_idempotency'
		)
	`, manifestDigest, generationID); err != nil {
		t.Fatalf("old native writer insert: %v", err)
	}
	_, err = tx.ExecContext(ctx, `SET CONSTRAINTS provider_operations_require_capacity_reservation IMMEDIATE`)
	if err == nil || !strings.Contains(err.Error(),
		"native provision operation requires an atomic managed runtime capacity reservation") {
		t.Fatalf("old native writer constraint error = %v, want atomic capacity fence", err)
	}
}

type historicalCapacityLease struct {
	TenantID           string
	OwnerID            string
	LeaseID            string
	ServerID           string
	GenerationID       string
	TypedProviderID    any
	ResourceProviderID string
}

func seedManagedRuntimeCapacityHistoricalNativeOperation(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	lease historicalCapacityLease,
) string {
	t.Helper()
	const (
		manifestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		commandDigest  = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		receiptDigest  = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		specDigest     = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	)
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin historical native operation seed: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, lease.TenantID); err != nil {
		t.Fatalf("set historical native operation tenant: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO runtime_lease_execution_authorities (
			tenant_id, lease_id, execution_authority, bound_at
		) VALUES ($1, $2, 'techstack_provider_control', clock_timestamp())
	`, lease.TenantID, lease.LeaseID); err != nil {
		t.Fatalf("seed historical native authority: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO provider_catalog_versions (catalog_version, status)
		VALUES ('catalog-capacity-claim', 'draft')
	`); err != nil {
		t.Fatalf("seed historical claim catalog version: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO provider_catalog_profiles (
			catalog_version, provider_id, adapter_id, credential_mode,
			runtime_profile_id, offering_id, can_pause, stop_effect,
			can_recreate, capability_snapshot, adapter_manifest_hash,
			provision_dispatch_mode
		) VALUES (
			'catalog-capacity-claim', 'ionos', 'ionos-v1', 'managed',
			'ionos-managed-pvm-monthly', 'monthly-runtime-standard',
			true, 'pause', true, '{}'::jsonb, $1, 'native_idempotency'
		)
	`, manifestDigest); err != nil {
		t.Fatalf("seed historical claim execution profile: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO provider_desired_spec_revisions (
			tenant_id, lease_id, revision, spec_ref, spec_digest, spec_json, created_at
		) VALUES ($1, $2, 1, $3, $4, '{}'::jsonb, clock_timestamp())
	`, lease.TenantID, lease.LeaseID,
		"desired-spec://capacity-claim/"+lease.LeaseID+"/revision-1", specDigest); err != nil {
		t.Fatalf("seed historical claim desired spec: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO provider_operations (
			tenant_id, operation_id, lease_id, operation, idempotency_key,
			adapter_id, command_digest, command_json, ledger_revision,
			desired_spec_revision, status, phase, head_sequence,
			head_receipt_digest, requested_at, created_at, updated_at,
			provision_dispatch_mode
		) VALUES (
			$1, 'operation-capacity-claim', $2, 'provision',
			'capacity-claim-idempotency', 'ionos-v1', $3,
			jsonb_build_object(
				'schema_version', 'techstack.provider-control-operation/v1',
				'execution_authority', 'techstack_provider_control',
				'execution_profile', jsonb_build_object(
					'catalog_version', 'catalog-capacity-claim',
					'provider_id', 'ionos',
					'adapter_id', 'ionos-v1',
					'credential_mode', 'managed',
					'runtime_profile_id', 'ionos-managed-pvm-monthly',
					'offering_id', 'monthly-runtime-standard',
					'provision_dispatch_mode', 'native_idempotency',
					'adapter_manifest_hash', $4::text
				),
				'command', jsonb_build_object(
					'resource_generation_id', $5::text
				)
			),
			1, 1, 'pending', 'requested', 1, $6,
			clock_timestamp(), clock_timestamp(), clock_timestamp(),
			'native_idempotency'
		)
	`, lease.TenantID, lease.LeaseID, commandDigest, manifestDigest,
		lease.GenerationID, receiptDigest); err != nil {
		t.Fatalf("seed historical native provision operation: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO provider_operation_receipts (
			tenant_id, operation_id, sequence, previous_receipt_digest,
			receipt_digest, status, phase, receipt_json, issued_at
		) VALUES (
			$1, 'operation-capacity-claim', 1, NULL, $2,
			'pending', 'requested', '{}'::jsonb, clock_timestamp()
		)
	`, lease.TenantID, receiptDigest); err != nil {
		t.Fatalf("seed historical native provision receipt: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit historical native operation seed: %v", err)
	}
	return receiptDigest
}

func seedManagedRuntimeCapacityHistoricalLease(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	lease historicalCapacityLease,
) {
	t.Helper()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin historical capacity seed: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, lease.TenantID); err != nil {
		t.Fatalf("set historical capacity tenant: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO techstack_tenants (id, display_name, kind, status)
		VALUES ($1, $1, 'saas', 'active')
	`, lease.TenantID); err != nil {
		t.Fatalf("seed historical capacity tenant: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO servers (
			id, tenant_id, owner_subject_id, lease_id, name,
			lifecycle_state, desired_state, connection_state, health_state
		) VALUES (
			$1, $2, $3, $4, $1, 'active', 'running', 'pending', 'unknown'
		)
	`, lease.ServerID, lease.TenantID, lease.OwnerID, lease.LeaseID); err != nil {
		t.Fatalf("seed historical capacity server: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO techstack_vm_leases (
			id, tenant_id, subject_id, org_id, provider_id, engine_vm_id,
			desired_state, idempotency_key, lease_json, lease_revision,
			owner_subject_id, server_id, resource_generation_id,
			valid_from, valid_until, renewed_at, created_at, updated_at
		) VALUES (
			$1, $2, $3, $2, $4, NULL,
			'running', $1 || '-idempotency',
			jsonb_build_object(
				'billing_mode', 'subscription',
				'lifecycle_class', 'subscription',
				'resource', jsonb_build_object('provider_id', $5::text),
				'metadata', jsonb_build_object('resource_generation_id', $6::text)
			),
			1, $3, $7, $6::uuid,
			clock_timestamp(), clock_timestamp() + interval '1 year',
			clock_timestamp(), clock_timestamp(), clock_timestamp()
		)
	`, lease.LeaseID, lease.TenantID, lease.OwnerID, lease.TypedProviderID,
		lease.ResourceProviderID, lease.GenerationID, lease.ServerID); err != nil {
		t.Fatalf("seed historical capacity lease: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit historical capacity seed: %v", err)
	}
}

func openManagedRuntimeCapacityPremigrationDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := integrationDSN()
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping managed capacity PostgreSQL integration test")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse managed capacity integration DSN: %v", err)
	}
	admin := stdlib.OpenDB(*config)
	if err := admin.PingContext(t.Context()); err != nil {
		_ = admin.Close()
		t.Fatalf("ping managed capacity integration PostgreSQL: %v", err)
	}
	schema := "managed_capacity_pre033_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.ExecContext(t.Context(), "CREATE SCHEMA "+quotedSchema); err != nil {
		_ = admin.Close()
		t.Fatalf("create pre-033 schema: %v", err)
	}

	scoped := config.Copy()
	scoped.RuntimeParams = make(map[string]string, len(config.RuntimeParams)+1)
	for key, value := range config.RuntimeParams {
		scoped.RuntimeParams[key] = value
	}
	scoped.RuntimeParams["search_path"] = schema + ",public"
	database := stdlib.OpenDB(*scoped)
	if err := database.PingContext(t.Context()); err != nil {
		_ = database.Close()
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		_ = admin.Close()
		t.Fatalf("open pre-033 schema: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		_ = admin.Close()
	})
	return database
}

func applyManagedRuntimeCapacityMigrationsBefore033(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
) {
	t.Helper()
	applyManagedRuntimeCapacityMigrationsBefore(t, ctx, database, "033_")
}

func applyManagedRuntimeCapacityMigrationsBefore(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	stopPrefix string,
) {
	t.Helper()
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() >= stopPrefix {
			continue
		}
		applyManagedRuntimeCapacityMigrationFile(t, ctx, database, "migrations/"+entry.Name())
	}
}

func applyManagedRuntimeCapacityMigrationFile(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	path string,
) {
	t.Helper()
	payload, err := migrationsFS.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin %s: %v", path, err)
	}
	if _, err := tx.ExecContext(ctx, string(payload)); err != nil {
		_ = tx.Rollback()
		t.Fatalf("apply %s: %v", path, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit %s: %v", path, err)
	}
}

func applyManagedRuntimeCapacityMigration033(ctx context.Context, database *sql.DB) error {
	payload, err := migrationsFS.ReadFile(managedRuntimeCapacityMigration)
	if err != nil {
		return fmt.Errorf("read migration 033: %w", err)
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration 033: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, string(payload)); err != nil {
		return err
	}
	return tx.Commit()
}
