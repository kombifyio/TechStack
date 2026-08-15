package db

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestProviderProvisionDispatchGuardMigrationIsHeadSpecificAndFailClosed(t *testing.T) {
	content := readDBFile(t, "migrations/029_provider_provision_dispatch_guards.sql")
	for _, required := range []string{
		"ALTER TABLE provider_catalog_profiles",
		"DISABLE TRIGGER provider_catalog_profiles_reject_update",
		"ENABLE TRIGGER provider_catalog_profiles_reject_update",
		"SET provision_dispatch_mode = 'blocked'",
		"new provider catalog profile requires an executable provision dispatch mode",
		"provider_operation_dispatch_mode_insert_guard",
		"provider operation must copy an executable catalog dispatch-mode pin",
		"provider operation dispatch mode does not match its immutable catalog profile",
		"ADD COLUMN IF NOT EXISTS provision_dispatch_mode text",
		"DISABLE TRIGGER provider_operations_immutable_update",
		"ALTER TABLE provider_operations NO FORCE ROW LEVEL SECURITY",
		"ALTER TABLE provider_operations DISABLE ROW LEVEL SECURITY",
		"ALTER TABLE provider_operations ENABLE ROW LEVEL SECURITY",
		"ALTER TABLE provider_operations FORCE ROW LEVEL SECURITY",
		"ENABLE TRIGGER provider_operations_immutable_update",
		"ALTER COLUMN provision_dispatch_mode SET NOT NULL",
		"'native_idempotency'",
		"'provider_correlation'",
		"'at_most_once_dispatch_manual_reconcile'",
		"CREATE TABLE IF NOT EXISTS provider_provision_dispatch_guards",
		"PRIMARY KEY (tenant_id, operation_id)",
		"UNIQUE (tenant_id, lease_id, resource_generation_id)",
		"resource_generation_id uuid NOT NULL",
		"FOREIGN KEY (tenant_id, operation_id, head_sequence, head_receipt_digest)",
		"REFERENCES provider_operation_receipts",
		"FOREIGN KEY (tenant_id, lease_id)",
		"REFERENCES techstack_vm_leases (tenant_id, id)",
		"FOR SHARE OF runtime_lease",
		"live_lease_revision IS DISTINCT FROM NEW.lease_revision",
		"live_server_id IS DISTINCT FROM NEW.server_id",
		"live_resource_generation_id IS DISTINCT FROM NEW.resource_generation_id",
		"ADD COLUMN IF NOT EXISTS adapter_manifest_hash text",
		"new provider catalog profile requires an immutable adapter manifest digest",
		"prepared_request_digest",
		"credential_version_hash",
		"provider_scope_hash",
		"correlation_hash",
		"adapter_manifest_hash",
		"guard_origin IN ('first_claim', 'migration_quarantine')",
		"operation.command_json->>'schema_version' = 'techstack.provider-control-operation/v1'",
		"operation.command_json->>'execution_authority' = 'techstack_provider_control'",
		"receipt.phase = 'accepted'",
		"ORDER BY receipt.sequence ASC",
		"provider_provision_dispatch_guard_validate_insert",
		"current_setting('app.provider_execution_claim_token', true)",
		"current_setting('app.provider_execution_claim_owner', true)",
		"NEW.guarded_at := clock_timestamp()",
		"receipt.receipt_json->'resources'",
		"jsonb_array_length(receipt_resources) <> 0",
		"dispatch guard lease, UUID, or execution-profile projection mismatch",
		"blocked provider operation cannot acquire execution custody",
		"blocked provider operation cannot enter accepted custody",
		"AMO provision accepted claim requires its exact first-claim dispatch guard",
		"provision accepted claim requires generation-bound dispatch custody",
		"pg_advisory_xact_lock",
		"AMO provision accepted transition requires its consumed first-claim guard",
		"provider provision dispatch guards are immutable",
		"ENABLE ROW LEVEL SECURITY",
		"FORCE ROW LEVEL SECURITY",
		"current_setting('app.tenant_id', true)",
		"REVOKE ALL ON FUNCTION provider_catalog_profile_insert_guard() FROM PUBLIC",
		"REVOKE ALL ON FUNCTION provider_operation_dispatch_mode_insert_guard() FROM PUBLIC",
		"REVOKE ALL ON FUNCTION provider_execution_immutable_update() FROM PUBLIC",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("provider provision dispatch migration missing %q", required)
		}
	}

	for _, forbidden := range []string{
		"not_applicable",
		"server_generation bigint",
		"ON DELETE CASCADE",
		"legacy_simulate",
		"provisioning-executor/v1",
		"DELETE FROM provider_provision_dispatch_guards",
		"SET guard_origin =",
		"SET dispatch_mode =",
		"retry_count",
		"retry_at",
		"reset_at",
	} {
		if strings.Contains(content, forbidden) {
			t.Errorf("provider provision dispatch migration introduced forbidden behavior via %q", forbidden)
		}
	}
}

func TestIntegrationProviderProvisionDispatchGuardSchema(t *testing.T) {
	db := openTestDB(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var catalogNullable, operationNullable string
	if err := db.QueryRowContext(ctx, `
		SELECT
			max(is_nullable) FILTER (
				WHERE table_name = 'provider_catalog_profiles'
				  AND column_name = 'provision_dispatch_mode'
			),
			max(is_nullable) FILTER (
				WHERE table_name = 'provider_operations'
				  AND column_name = 'provision_dispatch_mode'
			)
		FROM information_schema.columns
		WHERE table_schema = current_schema()
	`).Scan(&catalogNullable, &operationNullable); err != nil {
		t.Fatalf("inspect dispatch-mode columns: %v", err)
	}
	if catalogNullable != "NO" || operationNullable != "NO" {
		t.Fatalf("dispatch-mode nullability = catalog %q operation %q, want NO/NO", catalogNullable, operationNullable)
	}

	var rowSecurity, forceRowSecurity bool
	if err := db.QueryRowContext(ctx, `
		SELECT relrowsecurity, relforcerowsecurity
		FROM pg_class
		WHERE oid = 'provider_provision_dispatch_guards'::regclass
	`).Scan(&rowSecurity, &forceRowSecurity); err != nil {
		t.Fatalf("inspect dispatch-guard RLS: %v", err)
	}
	if !rowSecurity || !forceRowSecurity {
		t.Fatalf("dispatch-guard RLS = enabled %v forced %v, want true/true", rowSecurity, forceRowSecurity)
	}

	var immutableTriggers int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_trigger
		WHERE tgrelid = 'provider_provision_dispatch_guards'::regclass
		  AND NOT tgisinternal
		  AND tgname IN (
			'provider_provision_dispatch_guards_validate_insert',
			'provider_provision_dispatch_guards_reject_update',
			'provider_provision_dispatch_guards_reject_delete'
		  )
	`).Scan(&immutableTriggers); err != nil {
		t.Fatalf("inspect dispatch-guard triggers: %v", err)
	}
	if immutableTriggers != 3 {
		t.Fatalf("dispatch-guard runtime triggers = %d, want 3", immutableTriggers)
	}

	var generationUnique bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM pg_constraint
			WHERE conrelid = 'provider_provision_dispatch_guards'::regclass
			  AND contype = 'u'
			  AND pg_get_constraintdef(oid) =
				  'UNIQUE (tenant_id, lease_id, resource_generation_id)'
		)
	`).Scan(&generationUnique); err != nil {
		t.Fatalf("inspect dispatch-guard generation uniqueness: %v", err)
	}
	if !generationUnique {
		t.Fatal("dispatch guard is not unique for tenant/lease/resource generation")
	}
}

func TestIntegrationProviderProvisionDispatchBackfillIsQuarantined(t *testing.T) {
	db := openProviderDispatchPremigrationDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	applyProviderDispatchMigrationsBefore029(t, ctx, db)
	seedAcceptedProviderDispatchBefore029(t, ctx, db)

	migration := readDBFile(t, "migrations/029_provider_provision_dispatch_guards.sql")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin migration 029: %v", err)
	}
	if _, err := tx.ExecContext(ctx, migration); err != nil {
		_ = tx.Rollback()
		t.Fatalf("apply migration 029 over existing provider rows: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit migration 029: %v", err)
	}

	for table, query := range map[string]string{
		"catalog profile": `
			SELECT provision_dispatch_mode
			FROM provider_catalog_profiles
			WHERE catalog_version = 'catalog-pre029'`,
		"provider operation": `
			SELECT provision_dispatch_mode
			FROM provider_operations
			WHERE tenant_id = 'tenant-pre029' AND operation_id = 'operation-pre029'`,
	} {
		var mode string
		if err := db.QueryRowContext(ctx, query).Scan(&mode); err != nil {
			t.Fatalf("load %s dispatch mode: %v", table, err)
		}
		if mode != "blocked" {
			t.Fatalf("%s dispatch mode = %q, want blocked", table, mode)
		}
	}

	var origin string
	var tokenDigest, owner sql.NullString
	if err := db.QueryRowContext(ctx, `
		SELECT guard_origin, first_claim_token_digest, first_claim_owner
		FROM provider_provision_dispatch_guards
		WHERE tenant_id = 'tenant-pre029' AND operation_id = 'operation-pre029'
	`).Scan(&origin, &tokenDigest, &owner); err != nil {
		t.Fatalf("load quarantine guard: %v", err)
	}
	if origin != "migration_quarantine" || tokenDigest.Valid || owner.Valid {
		t.Fatalf("quarantine guard = origin %q token %v owner %v", origin, tokenDigest, owner)
	}

	if _, err := db.ExecContext(ctx, `
		UPDATE provider_provision_dispatch_guards
		SET guarded_at = clock_timestamp()
		WHERE tenant_id = 'tenant-pre029' AND operation_id = 'operation-pre029'
	`); err == nil || !strings.Contains(err.Error(), "dispatch guards are immutable") {
		t.Fatalf("quarantine guard update error = %v, want immutable rejection", err)
	}
}

func openProviderDispatchPremigrationDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := integrationDSN()
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping Postgres integration test")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse integration DSN: %v", err)
	}
	admin := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = admin.Close() })
	schema := "dispatch_pre029_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.ExecContext(t.Context(), "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create pre-029 schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
	})

	scoped := config.Copy()
	scoped.RuntimeParams = make(map[string]string, len(config.RuntimeParams)+1)
	for key, value := range config.RuntimeParams {
		scoped.RuntimeParams[key] = value
	}
	scoped.RuntimeParams["search_path"] = schema + ",public"
	db := stdlib.OpenDB(*scoped)
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(t.Context()); err != nil {
		t.Fatalf("ping pre-029 schema: %v", err)
	}
	return db
}

func applyProviderDispatchMigrationsBefore029(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() >= "029_" {
			continue
		}
		payload, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin %s: %v", entry.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, string(payload)); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply %s: %v", entry.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit %s: %v", entry.Name(), err)
		}
	}
}

func seedAcceptedProviderDispatchBefore029(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	const generationID = "11111111-1111-4111-8111-111111111111"
	const capabilityHash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const profileHash = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const requestedDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	const acceptedDigest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	const commandDigest = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	const specDigest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin pre-029 seed: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', 'tenant-pre029', true)`); err != nil {
		t.Fatalf("set pre-029 tenant: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO techstack_tenants (id, display_name)
		VALUES ('tenant-pre029', 'Pre 029 Tenant')
	`); err != nil {
		t.Fatalf("seed pre-029 tenant: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO provider_catalog_versions (catalog_version, status)
		VALUES ('catalog-pre029', 'draft')
	`); err != nil {
		t.Fatalf("seed pre-029 catalog version: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO provider_catalog_profiles (
			catalog_version, provider_id, adapter_id, credential_mode,
			runtime_profile_id, offering_id, can_pause, stop_effect,
			can_recreate, capability_snapshot
		) VALUES (
			'catalog-pre029', 'ionos', 'ionos-v6', 'managed',
			'ionos-managed-pvm-monthly', 'monthly-runtime-standard',
			false, 'destroy', true, '{}'::jsonb
		)
	`); err != nil {
		t.Fatalf("seed pre-029 catalog profile: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO servers (
			id, tenant_id, owner_subject_id, lease_id, name,
			lifecycle_state, desired_state, connection_state, health_state
		) VALUES (
			'server-pre029', 'tenant-pre029', 'owner-pre029', 'lease-pre029',
			'server-pre029', 'provisioning', 'running', 'pending', 'unknown'
		)
	`); err != nil {
		t.Fatalf("seed pre-029 server: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO techstack_vm_leases (
			id, tenant_id, provider_id, desired_state, idempotency_key, lease_json,
			lease_revision, owner_subject_id, server_id, resource_generation_id,
			valid_from, valid_until, renewed_at
		) VALUES (
			'lease-pre029', 'tenant-pre029', 'ionos', 'running', 'lease-pre029-key',
			jsonb_build_object('metadata', jsonb_build_object('resource_generation_id', $1::text)),
			1, 'owner-pre029', 'server-pre029', $1::uuid,
			clock_timestamp(), clock_timestamp() + interval '1 year', clock_timestamp()
		)
	`, generationID); err != nil {
		t.Fatalf("seed pre-029 lease: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO runtime_lease_execution_authorities (
			tenant_id, lease_id, execution_authority, bound_at
		) VALUES (
			'tenant-pre029', 'lease-pre029', 'techstack_provider_control', clock_timestamp()
		)
	`); err != nil {
		t.Fatalf("seed pre-029 authority: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO provider_desired_spec_revisions (
			tenant_id, lease_id, revision, spec_ref, spec_digest, spec_json, created_at
		) VALUES (
			'tenant-pre029', 'lease-pre029', 1, 'desired-spec://techstack/lease-pre029/revision-1',
			$1, '{}'::jsonb, clock_timestamp()
		)
	`, specDigest); err != nil {
		t.Fatalf("seed pre-029 desired spec: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO provider_operations (
			tenant_id, operation_id, lease_id, operation, idempotency_key,
			adapter_id, command_digest, command_json, ledger_revision,
			desired_spec_revision, status, phase, head_sequence,
			head_receipt_digest, requested_at, created_at, updated_at
		) VALUES (
			'tenant-pre029', 'operation-pre029', 'lease-pre029', 'provision', 'provision-pre029',
			'ionos-v6', $1,
			jsonb_build_object(
				'schema_version', 'techstack.provider-control-operation/v1',
				'execution_authority', 'techstack_provider_control',
				'execution_profile', jsonb_build_object(
					'provider_id', 'ionos',
					'adapter_id', 'ionos-v6',
					'credential_mode', 'managed',
					'runtime_profile_id', 'ionos-managed-pvm-monthly',
					'offering_id', 'monthly-runtime-standard',
					'catalog_version', 'catalog-pre029',
					'capability_snapshot_hash', $2::text,
					'execution_profile_hash', $3::text
				),
				'command', jsonb_build_object(
					'lease_id', 'lease-pre029',
					'lease_revision', 1,
					'runtime_server_id', 'server-pre029',
					'resource_generation_id', $4::text,
					'capability_snapshot_hash', $2::text,
					'execution_profile_hash', $3::text
				)
			),
			1, 1, 'pending', 'accepted', 2, $5,
			clock_timestamp(), clock_timestamp(), clock_timestamp()
		)
	`, commandDigest, capabilityHash, profileHash, generationID, acceptedDigest); err != nil {
		t.Fatalf("seed pre-029 operation: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO provider_operation_receipts (
			tenant_id, operation_id, sequence, previous_receipt_digest,
			receipt_digest, status, phase, receipt_json, issued_at
		) VALUES
			('tenant-pre029', 'operation-pre029', 1, NULL, $1, 'pending', 'requested', '{}'::jsonb, clock_timestamp()),
			('tenant-pre029', 'operation-pre029', 2, $1, $2, 'pending', 'accepted', '{}'::jsonb, clock_timestamp())
	`, requestedDigest, acceptedDigest); err != nil {
		t.Fatalf("seed pre-029 receipts: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit pre-029 seed: %v", err)
	}
}

func TestProviderProvisionDispatchGuardMigrationKeepsModeAndGuardImmutable(t *testing.T) {
	content := readDBFile(t, "migrations/029_provider_provision_dispatch_guards.sql")

	if count := strings.Count(content, "guard.guard_origin = 'first_claim'"); count != 2 {
		t.Fatalf("first-claim authorization checks = %d, want 2 for active and consumed custody", count)
	}
	if !strings.Contains(content,
		"(to_jsonb(NEW) - ARRAY['status', 'phase', 'head_sequence', 'head_receipt_digest', 'updated_at'])") {
		t.Fatal("operation immutability guard no longer protects provision_dispatch_mode")
	}
	if !strings.Contains(content, "BEFORE UPDATE ON provider_provision_dispatch_guards") ||
		!strings.Contains(content, "BEFORE DELETE ON provider_provision_dispatch_guards") {
		t.Fatal("dispatch guard update/delete rejection is incomplete")
	}
	if !strings.Contains(content, "guard_origin = 'migration_quarantine'") ||
		!strings.Contains(content, "first_claim_token_digest IS NULL") ||
		!strings.Contains(content, "first_claim_owner IS NULL") {
		t.Fatal("migration quarantine is not a non-executable custody marker")
	}
}
