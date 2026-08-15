package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type executionAuthorityMigrationSeed struct {
	tenantID             string
	leaseID              string
	generationID         string
	outboxGenerationID   string
	status               string
	attempts             int
	metadata             map[string]string
	columnProviderRef    string
	leaseJSONProviderRef string
	requestProviderRef   string
}

func TestIntegrationVMLeaseExecutionAuthorityMigrationInventoriesExactCustodyAndQuarantinesExecutableWork(t *testing.T) {
	d := openTestDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	schemaName := "techstack_migration_019_" + suffix
	ownerRole := "techstack_migration_019_owner_" + suffix
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
	} else if _, err := d.ExecContext(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create integration schema: %v", err)
	}
	createdSchema = true

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
	for _, migration := range []string{
		"migrations/003_vm_leases.sql",
		"migrations/005_vm_lease_enrollment_outbox.sql",
		"migrations/016_vm_lease_resource_generation.sql",
		"migrations/017_vm_lease_enrollment_generation.sql",
	} {
		if _, err := tx.ExecContext(ctx, readEmbeddedMigrationForIntegration(t, migration)); err != nil {
			t.Fatalf("apply pre-019 migration %s: %v", migration, err)
		}
	}

	const tenantA = "tenant-authority-a"
	const tenantB = "tenant-authority-b"
	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantA); err != nil {
		t.Fatalf("set tenant A for migration seeds: %v", err)
	}

	claimGeneration := uuid.NewString()
	claimBlockedGeneration := uuid.NewString()
	resultGeneration := uuid.NewString()
	failedHandleGeneration := uuid.NewString()
	handleGeneration := uuid.NewString()
	mixedFailedTripleGeneration := uuid.NewString()
	mixedClaimResultGeneration := uuid.NewString()
	seeds := []executionAuthorityMigrationSeed{
		{
			tenantID: tenantA, leaseID: "lease-claim-active", generationID: claimGeneration,
			outboxGenerationID: claimGeneration, status: "pending",
			metadata: map[string]string{
				"executor_claim_status": "active", "executor_claim_resource_generation_id": claimGeneration,
				"executor_claim_operation_id": "op-claim-active", "executor_claim_operation": "provision",
			},
		},
		{
			tenantID: tenantA, leaseID: "lease-claim-blocked", generationID: claimBlockedGeneration,
			outboxGenerationID: claimBlockedGeneration, status: "retrying", attempts: 2,
			metadata: map[string]string{
				"executor_claim_status": "blocked", "executor_claim_resource_generation_id": claimBlockedGeneration,
				"executor_claim_operation_id": "op-claim-blocked", "executor_claim_operation": "decommission",
			},
		},
		{
			tenantID: tenantA, leaseID: "lease-result", generationID: resultGeneration,
			outboxGenerationID: resultGeneration, status: "pending",
			metadata: map[string]string{
				"executor_last_resource_generation_id": resultGeneration, "executor_last_operation_id": "op-result",
				"executor_last_operation": "provision", "executor_last_status": "succeeded",
			},
		},
		{
			tenantID: tenantA, leaseID: "lease-result-failed-handle", generationID: failedHandleGeneration,
			outboxGenerationID: failedHandleGeneration, status: "retrying", attempts: 4,
			metadata: map[string]string{
				"executor_last_resource_generation_id": failedHandleGeneration, "executor_last_operation_id": "op-failed-handle",
				"executor_last_operation": "reconcile", "executor_last_status": "failed",
				"executor_last_provider_resource_ref": "node-failed",
			},
			columnProviderRef: "node-failed", leaseJSONProviderRef: "node-failed",
		},
		{
			tenantID: tenantA, leaseID: "lease-result-denied", generationID: uuid.NewString(),
			status: "pending", attempts: 9,
			metadata: map[string]string{
				"executor_last_operation_id": "op-denied", "executor_last_operation": "provision",
				"executor_last_status": "denied",
			},
		},
		{
			tenantID: tenantA, leaseID: "lease-result-plan", generationID: uuid.NewString(),
			status: "pending", attempts: 10,
			metadata: map[string]string{
				"executor_last_operation_id": "op-plan", "executor_last_operation": "plan",
				"executor_last_status": "succeeded",
			},
		},
		{
			tenantID: tenantA, leaseID: "lease-claim-observe", generationID: uuid.NewString(),
			status: "pending", attempts: 11,
			metadata: map[string]string{
				"executor_claim_status": "active", "executor_claim_operation_id": "op-observe",
				"executor_claim_operation": "observe",
			},
		},
		{
			tenantID: tenantA, leaseID: "lease-result-failed-no-handle", generationID: uuid.NewString(),
			status: "retrying", attempts: 12,
			metadata: map[string]string{
				"executor_last_operation_id": "op-failed-no-handle", "executor_last_operation": "reconcile",
				"executor_last_status": "failed",
			},
		},
		{
			tenantID: tenantA, leaseID: "lease-provider-handle", generationID: handleGeneration,
			outboxGenerationID: handleGeneration, status: "retrying", attempts: 3,
			columnProviderRef: "node-exact", leaseJSONProviderRef: "node-exact", requestProviderRef: "node-exact",
		},
		{
			tenantID: tenantA, leaseID: "lease-mixed-stale-failed-triple", generationID: mixedFailedTripleGeneration,
			outboxGenerationID: mixedFailedTripleGeneration, status: "retrying", attempts: 13,
			metadata: map[string]string{
				"executor_last_resource_generation_id": mixedFailedTripleGeneration,
				"executor_last_operation_id":           "op-stale-failed",
				"executor_last_operation":              "reconcile",
				"executor_last_status":                 "failed",
				"executor_last_provider_resource_ref":  "node-stale-mismatch",
			},
			columnProviderRef: "node-triple", leaseJSONProviderRef: "node-triple", requestProviderRef: "node-triple",
		},
		{
			tenantID: tenantA, leaseID: "lease-mixed-stale-claim-result", generationID: mixedClaimResultGeneration,
			outboxGenerationID: mixedClaimResultGeneration, status: "pending", attempts: 14,
			metadata: map[string]string{
				"executor_claim_status":                 "active",
				"executor_claim_resource_generation_id": uuid.NewString(),
				"executor_claim_operation_id":           "op-stale-claim",
				"executor_claim_operation":              "provision",
				"executor_last_resource_generation_id":  mixedClaimResultGeneration,
				"executor_last_operation_id":            "op-terminal-result",
				"executor_last_operation":               "reconcile",
				"executor_last_status":                  "succeeded",
			},
		},
		{
			tenantID: tenantA, leaseID: "lease-claim-wrong-generation", generationID: uuid.NewString(),
			outboxGenerationID: "", status: "pending", attempts: 5,
			metadata: map[string]string{
				"executor_claim_status": "active", "executor_claim_resource_generation_id": uuid.NewString(),
				"executor_claim_operation_id": "op-wrong-generation", "executor_claim_operation": "provision",
			},
		},
		{
			tenantID: tenantA, leaseID: "lease-result-wrong-operation", generationID: uuid.NewString(),
			outboxGenerationID: "", status: "pending", attempts: 6,
			metadata: map[string]string{
				"executor_last_operation_id": "op-wrong-operation", "executor_last_operation": "create",
				"executor_last_status": "succeeded",
			},
		},
		{
			tenantID: tenantA, leaseID: "lease-provider-handle-mismatch", generationID: uuid.NewString(),
			outboxGenerationID: "", status: "retrying", attempts: 7,
			columnProviderRef: "node-column", leaseJSONProviderRef: "node-column", requestProviderRef: "node-request",
		},
		{
			tenantID: tenantA, leaseID: "lease-outbox-wrong-generation", generationID: uuid.NewString(),
			outboxGenerationID: uuid.NewString(), status: "pending", attempts: 8,
			metadata: map[string]string{
				"executor_claim_status": "active", "executor_claim_operation_id": "op-outbox-generation",
				"executor_claim_operation": "provision",
			},
		},
		{
			tenantID: tenantA, leaseID: "lease-state-only-pending", generationID: uuid.NewString(),
			outboxGenerationID: "", status: "pending", attempts: 21,
		},
		{
			tenantID: tenantA, leaseID: "lease-state-only-retrying", generationID: uuid.NewString(),
			outboxGenerationID: "", status: "retrying", attempts: 34,
		},
		{
			tenantID: tenantA, leaseID: "lease-drain-gate", generationID: uuid.NewString(),
			outboxGenerationID: "", status: "retrying", attempts: 35,
		},
		{
			tenantID: tenantA, leaseID: "lease-runtime-metadata-frozen", generationID: uuid.NewString(),
			outboxGenerationID: "", status: "enrolled", attempts: 36,
			metadata: map[string]string{"runtime_enrollment_status": "failed"},
		},
		{
			tenantID: tenantA, leaseID: "lease-unbound-enrolled", generationID: uuid.NewString(),
			outboxGenerationID: "", status: "enrolled", attempts: 55,
		},
		{
			tenantID: tenantA, leaseID: "lease-native-authority", generationID: uuid.NewString(),
			outboxGenerationID: "", status: "enrolled",
		},
	}
	for i := range seeds {
		seed := &seeds[i]
		if seed.outboxGenerationID == "" {
			seed.outboxGenerationID = seed.generationID
		}
		if (strings.HasPrefix(seed.leaseID, "lease-result-") || seed.leaseID == "lease-result") && seed.metadata["executor_last_resource_generation_id"] == "" {
			seed.metadata["executor_last_resource_generation_id"] = seed.generationID
		}
		if seed.leaseID == "lease-claim-observe" {
			seed.metadata["executor_claim_resource_generation_id"] = seed.generationID
		}
		if seed.leaseID == "lease-outbox-wrong-generation" {
			seed.metadata["executor_claim_resource_generation_id"] = seed.generationID
		}
		insertExecutionAuthorityMigrationSeed(t, ctx, tx, *seed)
	}

	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantB); err != nil {
		t.Fatalf("set tenant B for migration seeds: %v", err)
	}
	tenantBGeneration := uuid.NewString()
	insertExecutionAuthorityMigrationSeed(t, ctx, tx, executionAuthorityMigrationSeed{
		tenantID: tenantB, leaseID: "lease-tenant-b-evidence", generationID: tenantBGeneration,
		outboxGenerationID: tenantBGeneration, status: "enrolled",
		metadata: map[string]string{
			"executor_last_resource_generation_id": tenantBGeneration, "executor_last_operation_id": "op-tenant-b",
			"executor_last_operation": "reconcile", "executor_last_status": "succeeded",
		},
	})
	insertExecutionAuthorityMigrationSeed(t, ctx, tx, executionAuthorityMigrationSeed{
		tenantID: tenantB, leaseID: "lease-tenant-b-unbound", generationID: uuid.NewString(),
		outboxGenerationID: uuid.NewString(), status: "enrolled",
	})

	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', '', true)"); err != nil {
		t.Fatalf("clear tenant before migration 019: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE techstack_vm_lease_enrollment_outbox
		SET updated_at = clock_timestamp(),
			next_attempt_at = clock_timestamp() + INTERVAL '15 minutes'
		WHERE tenant_id = $1 AND lease_id = 'lease-drain-gate'
	`, tenantA); err != nil {
		t.Fatalf("mark recent enrollment claim: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "SAVEPOINT migration_019_drain_gate"); err != nil {
		t.Fatalf("create migration 019 drain-gate savepoint: %v", err)
	}
	if _, err := tx.ExecContext(ctx, readEmbeddedMigrationForIntegration(t, "migrations/019_vm_lease_execution_authority.sql")); err == nil {
		_, _ = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT migration_019_drain_gate")
		t.Fatal("migration 019 accepted a recent pending/retrying enrollment claim")
	} else if !strings.Contains(err.Error(), "15-minute claim lease") {
		_, _ = tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT migration_019_drain_gate")
		t.Fatalf("migration 019 recent-claim rejection = %v, want 15-minute claim-lease diagnostic", err)
	}
	if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT migration_019_drain_gate"); err != nil {
		t.Fatalf("rollback migration 019 drain-gate savepoint: %v", err)
	}
	seedTime := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	if _, err := tx.ExecContext(ctx, `
		UPDATE techstack_vm_lease_enrollment_outbox
		SET updated_at = $3, next_attempt_at = $3
		WHERE tenant_id = $1 AND lease_id = $2
	`, tenantA, "lease-drain-gate", seedTime); err != nil {
		t.Fatalf("expire enrollment claim before migration 019: %v", err)
	}
	if _, err := tx.ExecContext(ctx, readEmbeddedMigrationForIntegration(t, "migrations/019_vm_lease_execution_authority.sql")); err != nil {
		t.Fatalf("apply migration 019: %v", err)
	}

	assertExecutionAuthorityTableContract(t, ctx, tx, schemaName)
	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantA); err != nil {
		t.Fatalf("set tenant A after migration: %v", err)
	}
	for _, leaseID := range []string{
		"lease-claim-active",
		"lease-claim-blocked",
		"lease-result",
		"lease-result-failed-handle",
		"lease-provider-handle",
		"lease-mixed-stale-failed-triple",
		"lease-mixed-stale-claim-result",
	} {
		assertExecutionAuthority(t, ctx, tx, tenantA, leaseID, "legacy_simulate")
	}
	var evidenceGeneration, evidenceKind, provenance string
	if err := tx.QueryRowContext(ctx, `
		SELECT evidence_resource_generation_id,
		       evidence_json->>'evidence_kind',
		       lease.lease_json->'metadata'->>'runtime_execution_authority_provenance'
		FROM runtime_lease_execution_authorities AS authority
		JOIN techstack_vm_leases AS lease
		  ON lease.tenant_id = authority.tenant_id AND lease.id = authority.lease_id
		WHERE authority.tenant_id = $1 AND authority.lease_id = 'lease-provider-handle'
	`, tenantA).Scan(&evidenceGeneration, &evidenceKind, &provenance); err != nil {
		t.Fatalf("read immutable handle-custody evidence: %v", err)
	}
	if evidenceGeneration != handleGeneration || evidenceKind != "provider_handle_triple_match" || provenance != "legacy_simulate" {
		t.Fatalf("handle-custody evidence = generation:%q kind:%q provenance:%q", evidenceGeneration, evidenceKind, provenance)
	}
	assertExecutionAuthorityEvidence(t, ctx, tx, tenantA, "lease-mixed-stale-failed-triple", map[string]string{
		"evidence_kind":         "provider_handle_triple_match",
		"provider_resource_ref": "node-triple",
		"lease_resource_ref":    "node-triple",
		"request_resource_ref":  "node-triple",
		"operation":             "",
		"operation_id":          "",
		"result_status":         "",
	})
	assertExecutionAuthorityEvidence(t, ctx, tx, tenantA, "lease-mixed-stale-claim-result", map[string]string{
		"evidence_kind":         "succeeded_side_effect_result",
		"operation":             "reconcile",
		"operation_id":          "op-terminal-result",
		"result_status":         "succeeded",
		"claim_status":          "",
		"provider_resource_ref": "",
	})
	for _, leaseID := range []string{
		"lease-result-denied",
		"lease-result-plan",
		"lease-claim-observe",
		"lease-result-failed-no-handle",
		"lease-claim-wrong-generation",
		"lease-result-wrong-operation",
		"lease-provider-handle-mismatch",
		"lease-outbox-wrong-generation",
		"lease-state-only-pending",
		"lease-state-only-retrying",
		"lease-drain-gate",
		"lease-runtime-metadata-frozen",
		"lease-unbound-enrolled",
		"lease-native-authority",
	} {
		assertExecutionAuthority(t, ctx, tx, tenantA, leaseID, "")
	}

	for leaseID, wantAttempts := range map[string]int{
		"lease-claim-active":              0,
		"lease-claim-blocked":             2,
		"lease-result":                    0,
		"lease-result-failed-handle":      4,
		"lease-result-denied":             9,
		"lease-result-plan":               10,
		"lease-claim-observe":             11,
		"lease-result-failed-no-handle":   12,
		"lease-provider-handle":           3,
		"lease-mixed-stale-failed-triple": 13,
		"lease-mixed-stale-claim-result":  14,
		"lease-claim-wrong-generation":    5,
		"lease-result-wrong-operation":    6,
		"lease-provider-handle-mismatch":  7,
		"lease-outbox-wrong-generation":   8,
		"lease-state-only-pending":        21,
		"lease-state-only-retrying":       34,
		"lease-drain-gate":                35,
	} {
		assertQuarantinedExecutionAuthoritySeed(t, ctx, tx, tenantA, leaseID, wantAttempts)
	}
	var enrolledStatus, enrolledError string
	var enrolledAttempts int
	if err := tx.QueryRowContext(ctx, `
		SELECT status, COALESCE(last_error, ''), attempts
		FROM techstack_vm_lease_enrollment_outbox
		WHERE tenant_id = $1 AND lease_id = 'lease-unbound-enrolled'
	`, tenantA).Scan(&enrolledStatus, &enrolledError, &enrolledAttempts); err != nil {
		t.Fatalf("read unbound enrolled row: %v", err)
	}
	if enrolledStatus != "enrolled" || enrolledError != "" || enrolledAttempts != 55 {
		t.Fatalf("unbound enrolled row = status:%q error:%q attempts:%d, want enrolled/empty/55", enrolledStatus, enrolledError, enrolledAttempts)
	}

	var crossTenantVisible int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM runtime_lease_execution_authorities WHERE tenant_id = $1
	`, tenantB).Scan(&crossTenantVisible); err != nil {
		t.Fatalf("count cross-tenant authorities: %v", err)
	}
	if crossTenantVisible != 0 {
		t.Fatalf("tenant A can see %d tenant B authority rows, want 0", crossTenantVisible)
	}
	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantB); err != nil {
		t.Fatalf("set tenant B after migration: %v", err)
	}
	assertExecutionAuthority(t, ctx, tx, tenantB, "lease-tenant-b-evidence", "legacy_simulate")

	if _, err := tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantA); err != nil {
		t.Fatalf("restore tenant A after isolation check: %v", err)
	}
	assertStatementRejected(t, ctx, tx, "migration_019_invalid_authority", `
		INSERT INTO runtime_lease_execution_authorities (tenant_id, lease_id, execution_authority, bound_at)
		VALUES ($1, 'lease-native-authority', 'executor_ready', now())
	`, tenantA)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO runtime_lease_execution_authorities (tenant_id, lease_id, execution_authority, bound_at)
		VALUES ($1, 'lease-native-authority', 'techstack_provider_control', now())
	`, tenantA); err != nil {
		t.Fatalf("insert native execution authority: %v", err)
	}
	assertExecutionAuthority(t, ctx, tx, tenantA, "lease-native-authority", "techstack_provider_control")
	assertStatementRejected(t, ctx, tx, "migration_019_fresh_legacy_authority", `
		INSERT INTO runtime_lease_execution_authorities (
			tenant_id, lease_id, execution_authority, bound_at,
			evidence_resource_generation_id, evidence_json
		) VALUES (
			$1, 'lease-runtime-metadata-frozen', 'legacy_simulate', now(), $2,
			jsonb_build_object('schema_version', 'fabricated')
		)
	`, tenantA, uuid.NewString())
	assertStatementRejected(t, ctx, tx, "migration_019_generation_not_identity", `
		INSERT INTO runtime_lease_execution_authorities (tenant_id, lease_id, execution_authority, bound_at)
		VALUES ($1, 'lease-native-authority', 'legacy_simulate', now())
	`, tenantA)
	assertStatementRejected(t, ctx, tx, "migration_019_authority_update", `
		UPDATE runtime_lease_execution_authorities
		SET execution_authority = 'legacy_simulate'
		WHERE tenant_id = $1 AND lease_id = 'lease-native-authority'
	`, tenantA)
	assertStatementRejected(t, ctx, tx, "migration_019_authority_delete", `
		DELETE FROM runtime_lease_execution_authorities
		WHERE tenant_id = $1 AND lease_id = 'lease-native-authority'
	`, tenantA)

	assertStatementRejected(t, ctx, tx, "migration_019_legacy_outbox_insert", `
		INSERT INTO techstack_vm_lease_enrollment_outbox (
			tenant_id, lease_id, resource_generation_id, request_json, status
		) VALUES ($1, 'lease-new-legacy-outbox', $2, '{}'::jsonb, 'pending')
	`, tenantA, uuid.NewString())
	assertStatementRejected(t, ctx, tx, "migration_019_legacy_outbox_update", `
		UPDATE techstack_vm_lease_enrollment_outbox
		SET status = 'retrying'
		WHERE tenant_id = $1 AND lease_id = 'lease-state-only-pending'
	`, tenantA)
	assertStatementRejected(t, ctx, tx, "migration_019_legacy_outbox_delete", `
		DELETE FROM techstack_vm_lease_enrollment_outbox
		WHERE tenant_id = $1 AND lease_id = 'lease-state-only-pending'
	`, tenantA)

	assertStatementRejected(t, ctx, tx, "migration_019_executor_metadata_create", `
		UPDATE techstack_vm_leases
		SET lease_json = jsonb_set(lease_json, '{metadata,executor_claim_status}', '"active"'::jsonb, true)
		WHERE tenant_id = $1 AND id = 'lease-native-authority'
	`, tenantA)
	assertStatementRejected(t, ctx, tx, "migration_019_executor_metadata_change", `
		UPDATE techstack_vm_leases
		SET lease_json = jsonb_set(lease_json, '{metadata,executor_claim_status}', '"blocked"'::jsonb, true)
		WHERE tenant_id = $1 AND id = 'lease-claim-active'
	`, tenantA)
	assertStatementRejected(t, ctx, tx, "migration_019_enrollment_metadata_reactivate", `
		UPDATE techstack_vm_leases
		SET lease_json = jsonb_set(lease_json, '{metadata,runtime_enrollment_status}', '"pending"'::jsonb, true)
		WHERE tenant_id = $1 AND id = 'lease-runtime-metadata-frozen'
	`, tenantA)
	assertStatementRejected(t, ctx, tx, "migration_019_legacy_generation_change", `
		UPDATE techstack_vm_leases
		SET lease_json = jsonb_set(lease_json, '{metadata,resource_generation_id}', to_jsonb($2::text), true)
		WHERE tenant_id = $1 AND id = 'lease-claim-active'
	`, tenantA, uuid.NewString())
	assertStatementRejected(t, ctx, tx, "migration_019_legacy_normalized_handle_change", `
		UPDATE techstack_vm_leases
		SET engine_vm_id = 'mutated-provider-handle'
		WHERE tenant_id = $1 AND id = 'lease-provider-handle'
	`, tenantA)
	assertStatementRejected(t, ctx, tx, "migration_019_legacy_json_handle_change", `
		UPDATE techstack_vm_leases
		SET lease_json = jsonb_set(lease_json, '{resource,engine_vm_id}', '"mutated-provider-handle"'::jsonb, true)
		WHERE tenant_id = $1 AND id = 'lease-provider-handle'
	`, tenantA)
	assertStatementRejected(t, ctx, tx, "migration_019_legacy_provenance_clear", `
		UPDATE techstack_vm_leases
		SET lease_json = lease_json #- '{metadata,runtime_execution_authority_provenance}'
		WHERE tenant_id = $1 AND id = 'lease-provider-handle'
	`, tenantA)
	assertStatementRejected(t, ctx, tx, "migration_019_legacy_metadata_insert", `
		INSERT INTO techstack_vm_leases (
			id, tenant_id, subject_id, org_id, provider_id, desired_state,
			idempotency_key, lease_json
		) VALUES (
			'lease-new-legacy-metadata', $1, 'subject-authority', $1,
			'centron-managed', 'running', 'idempotency-new-legacy-metadata',
			jsonb_build_object('metadata', jsonb_build_object('runtime_enrollment_status', 'pending'))
		)
	`, tenantA)

	if _, err := tx.ExecContext(ctx, `
		UPDATE techstack_vm_leases
		SET lease_json = jsonb_set(lease_json, '{metadata,native_inventory_note}', '"preserved"'::jsonb, true)
		WHERE tenant_id = $1 AND id = 'lease-claim-active'
	`, tenantA); err != nil {
		t.Fatalf("update unrelated metadata beside frozen legacy evidence: %v", err)
	}
	var nativeInventoryNote string
	if err := tx.QueryRowContext(ctx, `
		SELECT lease_json->'metadata'->>'native_inventory_note'
		FROM techstack_vm_leases
		WHERE tenant_id = $1 AND id = 'lease-claim-active'
	`, tenantA).Scan(&nativeInventoryNote); err != nil {
		t.Fatalf("read unrelated metadata beside frozen legacy evidence: %v", err)
	}
	if nativeInventoryNote != "preserved" {
		t.Fatalf("unrelated metadata beside frozen legacy evidence = %q, want preserved", nativeInventoryNote)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit migration transaction: %v", err)
	}
}

func assertExecutionAuthorityEvidence(
	t *testing.T,
	ctx context.Context,
	tx *sql.Tx,
	tenantID string,
	leaseID string,
	want map[string]string,
) {
	t.Helper()
	var evidenceJSON []byte
	if err := tx.QueryRowContext(ctx, `
		SELECT evidence_json
		FROM runtime_lease_execution_authorities
		WHERE tenant_id = $1 AND lease_id = $2
	`, tenantID, leaseID).Scan(&evidenceJSON); err != nil {
		t.Fatalf("read execution-authority evidence for %s/%s: %v", tenantID, leaseID, err)
	}
	var evidence map[string]any
	if err := json.Unmarshal(evidenceJSON, &evidence); err != nil {
		t.Fatalf("decode execution-authority evidence for %s/%s: %v", tenantID, leaseID, err)
	}
	for key, wantValue := range want {
		gotValue, _ := evidence[key].(string)
		if gotValue != wantValue {
			t.Fatalf("execution-authority evidence %s/%s %s = %q, want %q (receipt: %s)", tenantID, leaseID, key, gotValue, wantValue, evidenceJSON)
		}
	}
}

func insertExecutionAuthorityMigrationSeed(t *testing.T, ctx context.Context, tx *sql.Tx, seed executionAuthorityMigrationSeed) {
	t.Helper()
	metadata := make(map[string]string, len(seed.metadata)+1)
	for key, value := range seed.metadata {
		metadata[key] = value
	}
	metadata["resource_generation_id"] = seed.generationID
	leaseJSON, err := json.Marshal(map[string]any{
		"metadata": metadata,
		"resource": map[string]string{"engine_vm_id": seed.leaseJSONProviderRef},
	})
	if err != nil {
		t.Fatalf("marshal lease seed %s: %v", seed.leaseID, err)
	}
	requestJSON, err := json.Marshal(map[string]string{
		"tenant_id":    seed.tenantID,
		"lease_id":     seed.leaseID,
		"engine_vm_id": seed.requestProviderRef,
	})
	if err != nil {
		t.Fatalf("marshal enrollment seed %s: %v", seed.leaseID, err)
	}
	seedTime := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO techstack_vm_leases (
			id, tenant_id, subject_id, org_id, provider_id, engine_vm_id,
			desired_state, idempotency_key, lease_json, created_at, updated_at
		) VALUES ($1, $2, 'subject-authority', $2, 'centron-managed', NULLIF($3, ''),
			'running', $4, $5::jsonb, $6, $6)
	`, seed.leaseID, seed.tenantID, seed.columnProviderRef, "idempotency-"+seed.leaseID, leaseJSON, seedTime); err != nil {
		t.Fatalf("insert lease seed %s: %v", seed.leaseID, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO techstack_vm_lease_enrollment_outbox (
			tenant_id, lease_id, resource_generation_id, request_json, idempotency_key,
			status, attempts, next_attempt_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8, $8, $8)
	`, seed.tenantID, seed.leaseID, seed.outboxGenerationID, requestJSON,
		"enrollment-"+seed.leaseID, seed.status, seed.attempts, seedTime); err != nil {
		t.Fatalf("insert enrollment seed %s: %v", seed.leaseID, err)
	}
}

func assertExecutionAuthorityTableContract(t *testing.T, ctx context.Context, tx *sql.Tx, schemaName string) {
	t.Helper()
	var primaryKeyColumns string
	if err := tx.QueryRowContext(ctx, `
		SELECT string_agg(att.attname, ',' ORDER BY key_column.ordinality)
		FROM pg_constraint AS con
		JOIN pg_class AS rel ON rel.oid = con.conrelid
		JOIN pg_namespace AS namespace ON namespace.oid = rel.relnamespace
		CROSS JOIN LATERAL unnest(con.conkey) WITH ORDINALITY AS key_column(attnum, ordinality)
		JOIN pg_attribute AS att ON att.attrelid = rel.oid AND att.attnum = key_column.attnum
		WHERE namespace.nspname = $1
		  AND rel.relname = 'runtime_lease_execution_authorities'
		  AND con.contype = 'p'
	`, schemaName).Scan(&primaryKeyColumns); err != nil {
		t.Fatalf("read execution-authority primary key: %v", err)
	}
	if primaryKeyColumns != "tenant_id,lease_id" {
		t.Fatalf("execution-authority primary key = %q, want tenant_id,lease_id", primaryKeyColumns)
	}

	var rowSecurity, forceRowSecurity bool
	if err := tx.QueryRowContext(ctx, `
		SELECT relrowsecurity, relforcerowsecurity
		FROM pg_class AS rel
		JOIN pg_namespace AS namespace ON namespace.oid = rel.relnamespace
		WHERE namespace.nspname = $1 AND rel.relname = 'runtime_lease_execution_authorities'
	`, schemaName).Scan(&rowSecurity, &forceRowSecurity); err != nil {
		t.Fatalf("read execution-authority RLS state: %v", err)
	}
	if !rowSecurity || !forceRowSecurity {
		t.Fatalf("execution-authority RLS = enabled:%t forced:%t, want true/true", rowSecurity, forceRowSecurity)
	}

	var policyUsing, policyCheck string
	if err := tx.QueryRowContext(ctx, `
		SELECT qual, with_check
		FROM pg_policies
		WHERE schemaname = $1
		  AND tablename = 'runtime_lease_execution_authorities'
		  AND policyname = 'tenant_isolation'
	`, schemaName).Scan(&policyUsing, &policyCheck); err != nil {
		t.Fatalf("read execution-authority tenant policy: %v", err)
	}
	for _, definition := range []string{policyUsing, policyCheck} {
		if !strings.Contains(definition, "app.tenant_id") || !strings.Contains(definition, "tenant_id") {
			t.Fatalf("execution-authority tenant policy is not tenant-bound: %q", definition)
		}
	}
}

func assertExecutionAuthority(t *testing.T, ctx context.Context, tx *sql.Tx, tenantID, leaseID, want string) {
	t.Helper()
	var got string
	err := tx.QueryRowContext(ctx, `
		SELECT execution_authority
		FROM runtime_lease_execution_authorities
		WHERE tenant_id = $1 AND lease_id = $2
	`, tenantID, leaseID).Scan(&got)
	if want == "" {
		if err != sql.ErrNoRows {
			t.Fatalf("execution authority for %s/%s = %q, err=%v, want unbound", tenantID, leaseID, got, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("read execution authority for %s/%s: %v", tenantID, leaseID, err)
	}
	if got != want {
		t.Fatalf("execution authority for %s/%s = %q, want %q", tenantID, leaseID, got, want)
	}
}

func assertQuarantinedExecutionAuthoritySeed(t *testing.T, ctx context.Context, tx *sql.Tx, tenantID, leaseID string, wantAttempts int) {
	t.Helper()
	var status, lastError string
	var attempts int
	var nextAttemptAt, createdAt time.Time
	if err := tx.QueryRowContext(ctx, `
		SELECT status, COALESCE(last_error, ''), attempts, next_attempt_at, created_at
		FROM techstack_vm_lease_enrollment_outbox
		WHERE tenant_id = $1 AND lease_id = $2
	`, tenantID, leaseID).Scan(&status, &lastError, &attempts, &nextAttemptAt, &createdAt); err != nil {
		t.Fatalf("read quarantined enrollment %s/%s: %v", tenantID, leaseID, err)
	}
	if status != "failed" || !strings.Contains(strings.ToLower(lastError), "execution") {
		t.Fatalf("quarantined enrollment %s/%s = status:%q error:%q, want failed with an execution-quarantine reason", tenantID, leaseID, status, lastError)
	}
	seedTime := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	if attempts != wantAttempts || !nextAttemptAt.Equal(seedTime) || !createdAt.Equal(seedTime) {
		t.Fatalf("quarantined enrollment %s/%s scheduling evidence changed: attempts:%d next:%s created:%s", tenantID, leaseID, attempts, nextAttemptAt, createdAt)
	}
}
