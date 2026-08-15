package db

import (
	"context"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

func TestWizardRunsMigrationIsTenantScopedWithKeyedLedger(t *testing.T) {
	content := readDBFile(t, "migrations/045_wizard_runs.sql")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS wizard_runs",
		"REFERENCES techstack_tenants(id) ON DELETE CASCADE",
		"uq_wizard_runs_idempotency",
		"WHERE idempotency_key IS NOT NULL",
		"FOREIGN KEY (tenant_id, homelab_id)",
		"REFERENCES homelabs (tenant_id, id)",
		"ON DELETE RESTRICT",
		"ENABLE ROW LEVEL SECURITY",
		"FORCE ROW LEVEL SECURITY",
		"CREATE POLICY tenant_isolation",
		"CREATE TRIGGER set_wizard_runs_updated_at",
		"CHECK (run_kind IN ('first-run', 'expansion'))",
		"CHECK (status IN ('completed', 'failed'))",
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

// TestIntegrationWizardRunLedger exercises the 045 ledger against real
// PostgreSQL: the partial-unique ON CONFLICT upsert, the tenant/owner scoped
// key lookup, and the composite homelab FK on both wizard_runs and stacks.
func TestIntegrationWizardRunLedger(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	store := controlplane.NewPostgresStore(d.DB)

	const tenantID = "tenant-wizard-runs-1"
	if _, err := store.EnsureTenant(ctx, controlplane.Tenant{ID: tenantID}); err != nil {
		t.Fatalf("EnsureTenant: %v", err)
	}
	homelab, err := store.GetOrCreateHomelabForOwner(ctx, controlplane.CreateHomelabRequest{
		ID: "hl-wizard-runs-1", TenantID: tenantID, OwnerSubjectID: "auth0|wizard-user-1", Name: "homelab",
	})
	if err != nil {
		t.Fatalf("GetOrCreateHomelabForOwner: %v", err)
	}

	created, err := store.UpsertWizardRun(ctx, controlplane.WizardRun{
		ID: "run-int-1", TenantID: tenantID, OwnerSubjectID: "auth0|wizard-user-1",
		IdempotencyKey: "key-int-1", RequestSHA256: "hash-a",
		RunKind: "first-run", RequestedRunKind: "first-run",
		HomelabID: homelab.ID, StackID: "stack-int-1", Status: "completed",
		Intent: map[string]any{"intent": map[string]any{"name": "My Homelab"}},
		Result: map[string]any{"stack_id": "stack-int-1"},
	})
	if err != nil {
		t.Fatalf("UpsertWizardRun: %v", err)
	}
	if created.HomelabID != homelab.ID || created.IdempotencyKey != "key-int-1" {
		t.Fatalf("unexpected created run: %#v", created)
	}

	// Keyed retry replaces the row through the partial-unique conflict target
	// and keeps the original ledger id.
	updated, err := store.UpsertWizardRun(ctx, controlplane.WizardRun{
		ID: "run-int-2", TenantID: tenantID, OwnerSubjectID: "auth0|wizard-user-1",
		IdempotencyKey: "key-int-1", RequestSHA256: "hash-b",
		RunKind: "expansion", RequestedRunKind: "first-run",
		HomelabID: homelab.ID, StackID: "stack-int-2", Status: "completed",
	})
	if err != nil {
		t.Fatalf("keyed retry upsert: %v", err)
	}
	if updated.ID != "run-int-1" || updated.StackID != "stack-int-2" || updated.RequestSHA256 != "hash-b" {
		t.Fatalf("keyed retry did not replace in place: %#v", updated)
	}

	got, err := store.GetWizardRunByKey(ctx, tenantID, "auth0|wizard-user-1", "key-int-1")
	if err != nil {
		t.Fatalf("GetWizardRunByKey: %v", err)
	}
	if got.ID != "run-int-1" || got.RunKind != "expansion" {
		t.Fatalf("unexpected ledger read: %#v", got)
	}

	// The composite FK rejects a homelab reference that does not exist in the
	// tenant.
	if _, fkErr := store.UpsertWizardRun(ctx, controlplane.WizardRun{
		ID: "run-int-3", TenantID: tenantID, OwnerSubjectID: "auth0|wizard-user-1",
		RequestSHA256: "hash-c", RunKind: "expansion", RequestedRunKind: "expansion",
		HomelabID: "hl-does-not-exist", Status: "completed",
	}); fkErr == nil {
		t.Fatal("expected homelab FK violation, got nil")
	}

	// stacks.homelab_id round-trips through CreateStack and SetStackHomelab.
	stack, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-wizard-runs-1", TenantID: tenantID, OwnerSubjectID: "auth0|wizard-user-1",
		HomelabID: homelab.ID, Name: "wizard-runs-linked",
	})
	if err != nil {
		t.Fatalf("CreateStack with homelab: %v", err)
	}
	if stack.HomelabID != homelab.ID {
		t.Fatalf("CreateStack did not persist homelab link: %#v", stack)
	}
	legacy, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-wizard-runs-2", TenantID: tenantID, OwnerSubjectID: "auth0|wizard-user-1",
		Name: "wizard-runs-legacy",
	})
	if err != nil {
		t.Fatalf("CreateStack legacy: %v", err)
	}
	if legacy.HomelabID != "" {
		t.Fatalf("legacy stack must stay unlinked: %#v", legacy)
	}
	healed, err := store.SetStackHomelab(ctx, tenantID, legacy.ID, homelab.ID)
	if err != nil {
		t.Fatalf("SetStackHomelab: %v", err)
	}
	if healed.HomelabID != homelab.ID {
		t.Fatalf("SetStackHomelab did not link: %#v", healed)
	}
}
