package db

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestIntegrationVMLeaseEnrollmentGenerationMigrationBindsAndQuarantinesLegacyRows(t *testing.T) {
	d := openTestDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	schemaName := "techstack_migration_017_" + suffix
	ownerRole := "techstack_migration_017_owner_" + suffix
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
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if createdSchema {
			if _, err := d.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE"); err != nil {
				t.Errorf("drop integration schema %s: %v", schemaName, err)
			}
		}
		if createdRole {
			if _, err := d.ExecContext(cleanupCtx, "DROP ROLE IF EXISTS "+quotedOwner); err != nil {
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
	migration017 := readEmbeddedMigrationForIntegration(t, "migrations/017_vm_lease_enrollment_generation.sql")

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin migration transaction: %v", err)
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
		t.Fatalf("apply pre-017 enrollment outbox schema: %v", err)
	}

	insertPreMigrationLease(t, ctx, tx, "tenant-bound", "lease-bound", `{"metadata":{"preserved":"bound"}}`)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO techstack_vm_lease_enrollment_outbox (
			tenant_id, lease_id, request_json, idempotency_key, status,
			attempts, next_attempt_at, created_at, updated_at
		) VALUES
			(
				'tenant-bound', 'lease-bound',
				'{"tenant_id":"tenant-bound","lease_id":"lease-bound"}'::jsonb,
				'bound-create', 'pending', 0, now() - interval '1 minute', now(), now()
			),
			(
				'tenant-orphan', 'lease-orphan',
				'{"tenant_id":"tenant-orphan","lease_id":"lease-orphan"}'::jsonb,
				'orphan-create', 'pending', 0, now() - interval '1 minute', now(), now()
			)
	`); err != nil {
		t.Fatalf("seed pre-017 enrollment outbox: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', '', true)"); err != nil {
		t.Fatalf("clear tenant GUC before migrations: %v", err)
	}
	if _, err := tx.ExecContext(ctx, migration016); err != nil {
		t.Fatalf("apply migration 016: %v", err)
	}
	boundGeneration := readTenantGeneration(t, ctx, tx, "tenant-bound", "lease-bound")
	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', '', true)"); err != nil {
		t.Fatalf("clear tenant GUC before migration 017: %v", err)
	}
	if _, err := tx.ExecContext(ctx, migration017); err != nil {
		t.Fatalf("apply migration 017: %v", err)
	}

	assertVMLeaseRLSState(t, ctx, tx, schemaName, true, true)

	var gotGeneration, boundStatus, boundError string
	var boundClaimable bool
	if err := tx.QueryRowContext(ctx, `
		SELECT resource_generation_id, status, COALESCE(last_error, ''),
			status IN ('pending', 'retrying') AND next_attempt_at <= now()
		FROM techstack_vm_lease_enrollment_outbox
		WHERE tenant_id = 'tenant-bound' AND lease_id = 'lease-bound'
	`).Scan(&gotGeneration, &boundStatus, &boundError, &boundClaimable); err != nil {
		t.Fatalf("read generation-bound enrollment row: %v", err)
	}
	if gotGeneration != boundGeneration {
		t.Fatalf("outbox generation = %q, want exact lease generation %q", gotGeneration, boundGeneration)
	}
	if boundStatus != "pending" || boundError != "" || !boundClaimable {
		t.Fatalf("bound outbox = status:%q error:%q claimable:%t, want pending/empty/true", boundStatus, boundError, boundClaimable)
	}

	var orphanGeneration sql.NullString
	var orphanStatus, orphanError string
	var orphanClaimable bool
	if err := tx.QueryRowContext(ctx, `
		SELECT resource_generation_id, status, COALESCE(last_error, ''),
			status IN ('pending', 'retrying') AND next_attempt_at <= now()
		FROM techstack_vm_lease_enrollment_outbox
		WHERE tenant_id = 'tenant-orphan' AND lease_id = 'lease-orphan'
	`).Scan(&orphanGeneration, &orphanStatus, &orphanError, &orphanClaimable); err != nil {
		t.Fatalf("read quarantined orphan enrollment row: %v", err)
	}
	if orphanGeneration.Valid {
		t.Fatalf("orphan generation = %q, want NULL", orphanGeneration.String)
	}
	if orphanStatus != "failed" || orphanClaimable {
		t.Fatalf("orphan outbox = status:%q claimable:%t, want failed/false", orphanStatus, orphanClaimable)
	}
	if !strings.Contains(orphanError, "cannot be bound") || !strings.Contains(orphanError, "authoritative resource generation") {
		t.Fatalf("orphan error is not operator-readable: %q", orphanError)
	}

	var constraintValidated bool
	var constraintDefinition string
	if err := tx.QueryRowContext(ctx, `
		SELECT con.convalidated, pg_get_constraintdef(con.oid)
		FROM pg_constraint AS con
		JOIN pg_class AS rel ON rel.oid = con.conrelid
		JOIN pg_namespace AS n ON n.oid = rel.relnamespace
		WHERE n.nspname = $1
		  AND rel.relname = 'techstack_vm_lease_enrollment_outbox'
		  AND con.conname = 'techstack_vm_lease_enrollment_generation_uuid_check'
	`, schemaName).Scan(&constraintValidated, &constraintDefinition); err != nil {
		t.Fatalf("read enrollment generation constraint: %v", err)
	}
	if !constraintValidated {
		t.Fatal("enrollment generation constraint must be validated")
	}
	for _, fragment := range []string{"resource_generation_id", "failed"} {
		if !strings.Contains(constraintDefinition, fragment) {
			t.Fatalf("enrollment generation constraint missing %q: %s", fragment, constraintDefinition)
		}
	}
	assertStatementRejected(t, ctx, tx, "migration_017_pending_without_generation", `
		INSERT INTO techstack_vm_lease_enrollment_outbox (
			tenant_id, lease_id, resource_generation_id, request_json, status
		) VALUES (
			'tenant-invalid', 'lease-invalid', NULL, '{}'::jsonb, 'pending'
		)
	`)

	var claimableOrphans int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM techstack_vm_lease_enrollment_outbox
		WHERE tenant_id = 'tenant-orphan'
		  AND lease_id = 'lease-orphan'
		  AND status IN ('pending', 'retrying')
		  AND next_attempt_at <= now()
	`).Scan(&claimableOrphans); err != nil {
		t.Fatalf("count claimable orphan rows: %v", err)
	}
	if claimableOrphans != 0 {
		t.Fatalf("claimable orphan rows = %d, want 0", claimableOrphans)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit migration transaction: %v", err)
	}
}
