package db

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestIntegrationVMLeaseResourceGenerationMigrationBackfillsForcedRLSTenants(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	const callerSelectedGeneration = "00000000-0000-4000-8000-000000000001"

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	schemaName := "techstack_migration_016_" + suffix
	ownerRole := "techstack_migration_016_owner_" + suffix
	quotedSchema := quotePostgresTestIdentifier(schemaName)
	quotedOwner := quotePostgresTestIdentifier(ownerRole)

	var currentUser string
	var isSuperuser, bypassesRLS, canCreateRole bool
	if err := d.QueryRowContext(ctx, `
		SELECT rolname, rolsuper, rolbypassrls, rolcreaterole
		FROM pg_roles
		WHERE rolname = current_user
	`).Scan(&currentUser, &isSuperuser, &bypassesRLS, &canCreateRole); err != nil {
		t.Fatalf("read integration role attributes: %v", err)
	}

	useScopedOwner := isSuperuser || bypassesRLS
	createdRole := false
	createdSchema := false
	t.Cleanup(func() {
		if createdSchema {
			if _, err := d.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE"); err != nil {
				t.Errorf("drop integration schema %s: %v", schemaName, err)
			}
		}
		if createdRole {
			if _, err := d.ExecContext(context.Background(), "DROP ROLE IF EXISTS "+quotedOwner); err != nil {
				t.Errorf("drop integration role %s: %v", ownerRole, err)
			}
		}
	})

	if useScopedOwner {
		if !isSuperuser && !canCreateRole {
			t.Fatalf("integration role %q bypasses RLS but cannot create the unprivileged owner required for a meaningful FORCE RLS test", currentUser)
		}
		if _, err := d.ExecContext(ctx, "CREATE ROLE "+quotedOwner+" NOLOGIN NOSUPERUSER NOBYPASSRLS NOINHERIT"); err != nil {
			t.Fatalf("create unprivileged integration owner: %v", err)
		}
		createdRole = true
		if _, err := d.ExecContext(ctx, "GRANT "+quotedOwner+" TO "+quotePostgresTestIdentifier(currentUser)); err != nil {
			t.Fatalf("grant integration owner for SET ROLE: %v", err)
		}
		if _, err := d.ExecContext(ctx, "CREATE SCHEMA "+quotedSchema+" AUTHORIZATION "+quotedOwner); err != nil {
			t.Fatalf("create integration schema: %v", err)
		}
	} else {
		if _, err := d.ExecContext(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
			t.Fatalf("create integration schema: %v", err)
		}
	}
	createdSchema = true

	migration003 := readEmbeddedMigrationForIntegration(t, "migrations/003_vm_leases.sql")
	migration005 := readEmbeddedMigrationForIntegration(t, "migrations/005_vm_lease_enrollment_outbox.sql")
	migration016 := readEmbeddedMigrationForIntegration(t, "migrations/016_vm_lease_resource_generation.sql")

	tx, beginErr := d.BeginTx(ctx, nil)
	if beginErr != nil {
		t.Fatalf("begin migration transaction: %v", beginErr)
	}
	defer func() { _ = tx.Rollback() }()

	if useScopedOwner {
		if _, err := tx.ExecContext(ctx, "SET LOCAL ROLE "+quotedOwner); err != nil {
			t.Fatalf("set unprivileged migration owner: %v", err)
		}
	}
	if _, err := tx.ExecContext(ctx, "SET LOCAL search_path TO "+quotedSchema); err != nil {
		t.Fatalf("set migration search path: %v", err)
	}
	if _, err := tx.ExecContext(ctx, migration003); err != nil {
		t.Fatalf("apply pre-016 VM lease schema: %v", err)
	}
	if _, err := tx.ExecContext(ctx, migration005); err != nil {
		t.Fatalf("apply pre-016 journal schema: %v", err)
	}

	assertVMLeaseRLSState(t, ctx, tx, schemaName, true, true)
	insertPreMigrationLease(t, ctx, tx, "tenant-upgrade-a", "lease-upgrade-a", `{"metadata":{"preserved":"tenant-a"}}`)
	insertPreMigrationLease(t, ctx, tx, "tenant-upgrade-b", "lease-upgrade-b", `{"metadata":{"resource_generation_id":"`+callerSelectedGeneration+`","preserved":"tenant-b"}}`)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO techstack_vm_lease_operation_journal
			(tenant_id, lease_id, event_type, status, actor)
		VALUES
			('tenant-upgrade-a', 'lease-upgrade-a', 'decommission', 'decommissioned', 'legacy-upgrade-proof'),
			('tenant-upgrade-b', 'lease-upgrade-b', 'decommission', 'decommissioned', 'legacy-upgrade-proof')
	`); err != nil {
		t.Fatalf("seed legacy confirmed journal rows: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', '', true)"); err != nil {
		t.Fatalf("clear tenant GUC before migration: %v", err)
	}

	var tenantGUC string
	if err := tx.QueryRowContext(ctx, "SELECT current_setting('app.tenant_id', true)").Scan(&tenantGUC); err != nil {
		t.Fatalf("read tenant GUC before migration: %v", err)
	}
	if tenantGUC != "" {
		t.Fatalf("tenant GUC before migration = %q, want empty", tenantGUC)
	}

	// Execute the exact embedded production migration in one transaction, just
	// as DB.Migrate does. The table owner is subject to FORCE RLS here, so this
	// backfills zero rows unless the migration deliberately suspends RLS.
	if _, err := tx.ExecContext(ctx, migration016); err != nil {
		t.Fatalf("apply migration 016: %v", err)
	}

	assertVMLeaseRLSState(t, ctx, tx, schemaName, true, true)
	generationA := readTenantGeneration(t, ctx, tx, "tenant-upgrade-a", "lease-upgrade-a")
	generationB := readTenantGeneration(t, ctx, tx, "tenant-upgrade-b", "lease-upgrade-b")
	if generationA == generationB {
		t.Fatalf("resource generations must be unique across existing leases; both were %q", generationA)
	}
	if generationB == callerSelectedGeneration {
		t.Fatalf("caller-selected legacy resource generation survived migration: %q", generationB)
	}

	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', 'tenant-upgrade-a', true)"); err != nil {
		t.Fatalf("restore tenant A GUC: %v", err)
	}
	var preserved string
	if err := tx.QueryRowContext(ctx, `
		SELECT lease_json->'metadata'->>'preserved'
		FROM techstack_vm_leases
		WHERE id = 'lease-upgrade-a'
	`).Scan(&preserved); err != nil {
		t.Fatalf("read preserved metadata: %v", err)
	}
	if preserved != "tenant-a" {
		t.Fatalf("preserved metadata = %q, want tenant-a", preserved)
	}

	assertLeaseGenerationSchema(t, ctx, tx, schemaName)
	assertLeaseGenerationConstraints(t, ctx, tx, generationA)
	assertJournalGenerationSchema(t, ctx, tx, schemaName)
	assertJournalRLSState(t, ctx, tx, schemaName, true, true)
	assertJournalSecurityAndConstraints(t, ctx, tx)

	var legacyRows int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM techstack_vm_lease_operation_journal
		WHERE actor = 'legacy-upgrade-proof'
		  AND resource_generation_digest IS NULL
	`).Scan(&legacyRows); err != nil {
		t.Fatalf("read legacy confirmed journal row: %v", err)
	}
	if legacyRows != 1 {
		t.Fatalf("tenant A visible legacy confirmed journal rows = %d, want 1", legacyRows)
	}
	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', 'tenant-upgrade-b', true)"); err != nil {
		t.Fatalf("switch to tenant B GUC: %v", err)
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM techstack_vm_lease_operation_journal
		WHERE actor = 'legacy-upgrade-proof'
	`).Scan(&legacyRows); err != nil {
		t.Fatalf("read tenant B legacy confirmed journal row: %v", err)
	}
	if legacyRows != 1 {
		t.Fatalf("tenant B visible legacy confirmed journal rows = %d, want 1", legacyRows)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit migration transaction: %v", err)
	}

	// Verify the committed table is still FORCE-RLS protected and that an owner
	// session without tenant context cannot observe either tenant's row.
	verifyTx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin post-migration verification: %v", err)
	}
	defer func() { _ = verifyTx.Rollback() }()
	if useScopedOwner {
		if _, err := verifyTx.ExecContext(ctx, "SET LOCAL ROLE "+quotedOwner); err != nil {
			t.Fatalf("set post-migration owner role: %v", err)
		}
	}
	if _, err := verifyTx.ExecContext(ctx, "SET LOCAL search_path TO "+quotedSchema); err != nil {
		t.Fatalf("set post-migration search path: %v", err)
	}
	assertVMLeaseRLSState(t, ctx, verifyTx, schemaName, true, true)
	assertJournalRLSState(t, ctx, verifyTx, schemaName, true, true)
	if _, err := verifyTx.ExecContext(ctx, "SELECT set_config('app.tenant_id', '', true)"); err != nil {
		t.Fatalf("clear post-migration tenant GUC: %v", err)
	}
	var visibleWithoutTenant int
	if err := verifyTx.QueryRowContext(ctx, "SELECT COUNT(*) FROM techstack_vm_leases").Scan(&visibleWithoutTenant); err != nil {
		t.Fatalf("query leases without tenant context: %v", err)
	}
	if visibleWithoutTenant != 0 {
		t.Fatalf("leases visible without tenant context = %d, want 0", visibleWithoutTenant)
	}
	if err := verifyTx.QueryRowContext(ctx, "SELECT COUNT(*) FROM techstack_vm_lease_operation_journal").Scan(&visibleWithoutTenant); err != nil {
		t.Fatalf("query journal without tenant context: %v", err)
	}
	if visibleWithoutTenant != 0 {
		t.Fatalf("journal rows visible without tenant context = %d, want 0", visibleWithoutTenant)
	}
}

func insertPreMigrationLease(t *testing.T, ctx context.Context, tx *sql.Tx, tenantID, leaseID, leaseJSON string) {
	t.Helper()
	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID); err != nil {
		t.Fatalf("set tenant GUC for %s: %v", tenantID, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO techstack_vm_leases
			(id, tenant_id, subject_id, provider_id, desired_state, idempotency_key, lease_json)
		VALUES
			($1, $2, 'subject-upgrade', 'provider-upgrade', 'running', $3, $4::jsonb)
	`, leaseID, tenantID, "idempotency-"+leaseID, leaseJSON); err != nil {
		t.Fatalf("insert pre-migration lease %s: %v", leaseID, err)
	}
}

func readTenantGeneration(t *testing.T, ctx context.Context, tx *sql.Tx, tenantID, leaseID string) string {
	t.Helper()
	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID); err != nil {
		t.Fatalf("set tenant GUC for %s: %v", tenantID, err)
	}
	var generation string
	if err := tx.QueryRowContext(ctx, `
		SELECT lease_json->'metadata'->>'resource_generation_id'
		FROM techstack_vm_leases
		WHERE id = $1
	`, leaseID).Scan(&generation); err != nil {
		t.Fatalf("read resource generation for %s: %v", leaseID, err)
	}
	if strings.TrimSpace(generation) == "" {
		t.Fatalf("resource generation for %s is empty", leaseID)
	}
	if _, err := uuid.Parse(generation); err != nil {
		t.Fatalf("resource generation for %s is not a UUID: %q: %v", leaseID, generation, err)
	}
	return generation
}

func assertLeaseGenerationSchema(t *testing.T, ctx context.Context, tx *sql.Tx, schemaName string) {
	t.Helper()
	var validated bool
	var definition string
	if err := tx.QueryRowContext(ctx, `
		SELECT con.convalidated, pg_get_constraintdef(con.oid)
		FROM pg_constraint AS con
		JOIN pg_class AS rel ON rel.oid = con.conrelid
		JOIN pg_namespace AS n ON n.oid = rel.relnamespace
		WHERE n.nspname = $1
		  AND rel.relname = 'techstack_vm_leases'
		  AND con.conname = 'techstack_vm_leases_resource_generation_uuid_check'
	`, schemaName).Scan(&validated, &definition); err != nil {
		t.Fatalf("read lease generation UUID constraint: %v", err)
	}
	if !validated {
		t.Fatal("lease generation UUID constraint must be validated")
	}
	if !strings.Contains(definition, "resource_generation_id") {
		t.Fatalf("lease generation UUID constraint does not bind resource_generation_id: %s", definition)
	}

	for _, indexName := range []string{
		"idx_techstack_vm_leases_resource_generation",
		"idx_techstack_vm_leases_tenant_id",
	} {
		var unique, valid bool
		if err := tx.QueryRowContext(ctx, `
			SELECT idx.indisunique, idx.indisvalid
			FROM pg_index AS idx
			JOIN pg_class AS rel ON rel.oid = idx.indrelid
			JOIN pg_class AS index_rel ON index_rel.oid = idx.indexrelid
			JOIN pg_namespace AS n ON n.oid = rel.relnamespace
			WHERE n.nspname = $1
			  AND rel.relname = 'techstack_vm_leases'
			  AND index_rel.relname = $2
		`, schemaName, indexName).Scan(&unique, &valid); err != nil {
			t.Fatalf("read lease generation index %s: %v", indexName, err)
		}
		if !unique || !valid {
			t.Fatalf("lease generation index %s = unique:%t valid:%t, want true/true", indexName, unique, valid)
		}
	}
}

func assertLeaseGenerationConstraints(t *testing.T, ctx context.Context, tx *sql.Tx, existingGeneration string) {
	t.Helper()
	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', 'tenant-upgrade-a', true)"); err != nil {
		t.Fatalf("set tenant A for lease generation checks: %v", err)
	}
	assertStatementRejected(t, ctx, tx, "migration_016_non_uuid_generation", `
		INSERT INTO techstack_vm_leases
			(id, tenant_id, subject_id, provider_id, desired_state, idempotency_key, lease_json)
		VALUES (
			'lease-invalid-generation',
			'tenant-upgrade-a',
			'subject-upgrade',
			'provider-upgrade',
			'running',
			'idempotency-invalid-generation',
			jsonb_build_object('metadata', jsonb_build_object('resource_generation_id', 'caller-controlled'))
		)
	`)
	assertStatementRejected(t, ctx, tx, "migration_016_duplicate_generation", `
		INSERT INTO techstack_vm_leases
			(id, tenant_id, subject_id, provider_id, desired_state, idempotency_key, lease_json)
		VALUES (
			'lease-duplicate-generation',
			'tenant-upgrade-a',
			'subject-upgrade',
			'provider-upgrade',
			'running',
			'idempotency-duplicate-generation',
			jsonb_build_object('metadata', jsonb_build_object('resource_generation_id', $1))
		)
	`, existingGeneration)
}

func assertVMLeaseRLSState(t *testing.T, ctx context.Context, tx *sql.Tx, schemaName string, wantEnabled, wantForced bool) {
	t.Helper()
	assertTableRLSState(t, ctx, tx, schemaName, "techstack_vm_leases", wantEnabled, wantForced)
}

func assertJournalRLSState(t *testing.T, ctx context.Context, tx *sql.Tx, schemaName string, wantEnabled, wantForced bool) {
	t.Helper()
	assertTableRLSState(t, ctx, tx, schemaName, "techstack_vm_lease_operation_journal", wantEnabled, wantForced)
}

func assertTableRLSState(t *testing.T, ctx context.Context, tx *sql.Tx, schemaName, tableName string, wantEnabled, wantForced bool) {
	t.Helper()
	var enabled, forced bool
	if err := tx.QueryRowContext(ctx, `
		SELECT c.relrowsecurity, c.relforcerowsecurity
		FROM pg_class AS c
		JOIN pg_namespace AS n ON n.oid = c.relnamespace
		WHERE n.nspname = $1
		  AND c.relname = $2
	`, schemaName, tableName).Scan(&enabled, &forced); err != nil {
		t.Fatalf("read %s RLS state: %v", tableName, err)
	}
	if enabled != wantEnabled || forced != wantForced {
		t.Fatalf("%s RLS state = enabled:%t forced:%t, want enabled:%t forced:%t", tableName, enabled, forced, wantEnabled, wantForced)
	}
}

func assertJournalGenerationSchema(t *testing.T, ctx context.Context, tx *sql.Tx, schemaName string) {
	t.Helper()
	var dataType, nullable string
	if err := tx.QueryRowContext(ctx, `
		SELECT data_type, is_nullable
		FROM information_schema.columns
		WHERE table_schema = $1
		  AND table_name = 'techstack_vm_lease_operation_journal'
		  AND column_name = 'resource_generation_digest'
	`, schemaName).Scan(&dataType, &nullable); err != nil {
		t.Fatalf("read journal resource generation column: %v", err)
	}
	if dataType != "text" || nullable != "YES" {
		t.Fatalf("journal resource generation column = type:%s nullable:%s, want text/YES", dataType, nullable)
	}

	type constraintExpectation struct {
		kind      string
		validated bool
		found     bool
	}
	constraints := map[string]constraintExpectation{
		"techstack_vm_lease_operation_journal_generation_digest_check": {kind: "c", validated: true},
		"techstack_vm_lease_confirmed_generation_digest_check":         {kind: "c", validated: false},
		"techstack_vm_lease_operation_journal_tenant_lease_fkey":       {kind: "f", validated: false},
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT con.conname, con.contype::text, con.convalidated
		FROM pg_constraint AS con
		JOIN pg_class AS rel ON rel.oid = con.conrelid
		JOIN pg_namespace AS n ON n.oid = rel.relnamespace
		WHERE n.nspname = $1
		  AND rel.relname = 'techstack_vm_lease_operation_journal'
		  AND con.conname IN (
			'techstack_vm_lease_operation_journal_generation_digest_check',
			'techstack_vm_lease_confirmed_generation_digest_check',
			'techstack_vm_lease_operation_journal_tenant_lease_fkey'
		  )
	`, schemaName)
	if err != nil {
		t.Fatalf("query journal generation constraints: %v", err)
	}
	for rows.Next() {
		var name, kind string
		var validated bool
		if err := rows.Scan(&name, &kind, &validated); err != nil {
			t.Fatalf("scan journal generation constraint: %v", err)
		}
		expectation := constraints[name]
		expectation.found = true
		constraints[name] = expectation
		if kind != expectation.kind || validated != expectation.validated {
			t.Fatalf("journal constraint %s = kind:%s validated:%t, want kind:%s validated:%t", name, kind, validated, expectation.kind, expectation.validated)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate journal generation constraints: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close journal generation constraints: %v", err)
	}
	for name, expectation := range constraints {
		if !expectation.found {
			t.Fatalf("journal generation constraint %s is missing", name)
		}
	}

	var usingExpression, checkExpression string
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(qual, ''), COALESCE(with_check, '')
		FROM pg_policies
		WHERE schemaname = $1
		  AND tablename = 'techstack_vm_lease_operation_journal'
		  AND policyname = 'tenant_isolation'
	`, schemaName).Scan(&usingExpression, &checkExpression); err != nil {
		t.Fatalf("read journal tenant isolation policy: %v", err)
	}
	for label, expression := range map[string]string{"USING": usingExpression, "WITH CHECK": checkExpression} {
		if !strings.Contains(expression, "app.tenant_id") || !strings.Contains(expression, "tenant_id") {
			t.Fatalf("journal tenant isolation %s expression is incomplete: %s", label, expression)
		}
	}

	var triggerEnabled string
	if err := tx.QueryRowContext(ctx, `
		SELECT trigger.tgenabled::text
		FROM pg_trigger AS trigger
		JOIN pg_class AS rel ON rel.oid = trigger.tgrelid
		JOIN pg_namespace AS n ON n.oid = rel.relnamespace
		WHERE n.nspname = $1
		  AND rel.relname = 'techstack_vm_lease_operation_journal'
		  AND trigger.tgname = 'techstack_vm_lease_operation_journal_reject_mutation'
		  AND NOT trigger.tgisinternal
	`, schemaName).Scan(&triggerEnabled); err != nil {
		t.Fatalf("read journal append-only trigger: %v", err)
	}
	if triggerEnabled != "O" {
		t.Fatalf("journal append-only trigger enabled state = %q, want O", triggerEnabled)
	}

	var functionConfig string
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(array_to_string(proc.proconfig, ','), '')
		FROM pg_proc AS proc
		JOIN pg_namespace AS n ON n.oid = proc.pronamespace
		WHERE n.nspname = $1
		  AND proc.proname = 'techstack_vm_lease_operation_journal_reject_mutation'
	`, schemaName).Scan(&functionConfig); err != nil {
		t.Fatalf("read journal append-only function config: %v", err)
	}
	if !strings.Contains(functionConfig, "search_path=pg_catalog") {
		t.Fatalf("journal append-only function search path is not fixed: %q", functionConfig)
	}
}

func assertJournalSecurityAndConstraints(t *testing.T, ctx context.Context, tx *sql.Tx) {
	t.Helper()
	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', 'tenant-upgrade-a', true)"); err != nil {
		t.Fatalf("set tenant A for journal security checks: %v", err)
	}
	assertStatementRejected(t, ctx, tx, "migration_016_bad_digest", `
		INSERT INTO techstack_vm_lease_operation_journal
			(tenant_id, lease_id, event_type, status, resource_generation_digest)
		VALUES
			('tenant-upgrade-a', 'lease-upgrade-a', 'enrollment', 'enrolled', 'not-a-digest')
	`)
	assertStatementRejected(t, ctx, tx, "migration_016_missing_digest", `
		INSERT INTO techstack_vm_lease_operation_journal
			(tenant_id, lease_id, event_type, status)
		VALUES
			('tenant-upgrade-a', 'lease-upgrade-a', 'decommission', 'decommissioned')
	`)
	assertStatementRejected(t, ctx, tx, "migration_016_missing_lease", `
		INSERT INTO techstack_vm_lease_operation_journal
			(tenant_id, lease_id, event_type, status, resource_generation_digest)
		VALUES
			('tenant-upgrade-a', 'lease-does-not-exist', 'enrollment', 'enrolled', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')
	`)
	assertStatementRejected(t, ctx, tx, "migration_016_cross_tenant_append", `
		INSERT INTO techstack_vm_lease_operation_journal
			(tenant_id, lease_id, event_type, status, resource_generation_digest)
		VALUES
			('tenant-upgrade-b', 'lease-upgrade-b', 'enrollment', 'enrolled', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')
	`)

	var operationID int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO techstack_vm_lease_operation_journal
			(tenant_id, lease_id, event_type, status, actor, resource_generation_digest)
		VALUES
			('tenant-upgrade-a', 'lease-upgrade-a', 'decommission', 'decommissioned', 'current-tenant-append', $1)
		RETURNING id
	`, strings.Repeat("a", 64)).Scan(&operationID); err != nil {
		t.Fatalf("insert generation-bound confirmed journal row: %v", err)
	}
	assertStatementRejected(t, ctx, tx, "migration_016_update_journal", `
		UPDATE techstack_vm_lease_operation_journal
		SET actor = 'rewritten-custody'
		WHERE actor = 'current-tenant-append'
	`)
	assertStatementRejected(t, ctx, tx, "migration_016_delete_journal", `
		DELETE FROM techstack_vm_lease_operation_journal
		WHERE actor = 'current-tenant-append'
	`)
	var actor string
	if err := tx.QueryRowContext(ctx, `
		SELECT actor
		FROM techstack_vm_lease_operation_journal
		WHERE id = $1
	`, operationID).Scan(&actor); err != nil {
		t.Fatalf("read immutable journal append: %v", err)
	}
	if actor != "current-tenant-append" {
		t.Fatalf("journal append actor = %q after rejected mutations", actor)
	}

	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', 'tenant-upgrade-b', true)"); err != nil {
		t.Fatalf("set tenant B for journal FK check: %v", err)
	}
	assertStatementRejected(t, ctx, tx, "migration_016_mismatched_tenant_lease", `
		INSERT INTO techstack_vm_lease_operation_journal
			(tenant_id, lease_id, event_type, status, resource_generation_digest)
		VALUES
			('tenant-upgrade-b', 'lease-upgrade-a', 'enrollment', 'enrolled', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')
	`)
	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', 'tenant-upgrade-a', true)"); err != nil {
		t.Fatalf("restore tenant A after journal FK check: %v", err)
	}
}

func assertStatementRejected(t *testing.T, ctx context.Context, tx *sql.Tx, savepoint, statement string, args ...any) {
	t.Helper()
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+savepoint); err != nil {
		t.Fatalf("create savepoint %s: %v", savepoint, err)
	}
	if _, err := tx.ExecContext(ctx, statement, args...); err == nil {
		_, _ = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+savepoint)
		t.Fatalf("database accepted forbidden statement at savepoint %s", savepoint)
	}
	if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+savepoint); err != nil {
		t.Fatalf("rollback constraint savepoint %s: %v", savepoint, err)
	}
}

func readEmbeddedMigrationForIntegration(t *testing.T, name string) string {
	t.Helper()
	content, err := migrationsFS.ReadFile(name)
	if err != nil {
		t.Fatalf("read embedded migration %s: %v", name, err)
	}
	return string(content)
}

func quotePostgresTestIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
