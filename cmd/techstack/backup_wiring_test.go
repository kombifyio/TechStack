package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	storageauth "github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/orchestrator"
)

// A managed Cloud restore drill must reach the entitlement gate that the boot
// installs. Before the fix the boot handed the backup wiring a nil feature
// service (the route build owns its dependencies by value), so no admission was
// installed and every drill failed as "managed backup admission unavailable",
// even for an owner the gate would have judged. An owner without the backup
// entitlement must receive the structured denial, not an unwired gate.
func TestBootedBackupWiringJudgesAManagedRestoreDrillByEntitlement(t *testing.T) {
	t.Setenv("TECHSTACK_RUNTIME_E2E_TENANT_ID", "")
	ctx := context.Background()

	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Cloud",
		StackKitInstanceID: "cloud", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	binding := jobs.StackKitRolloutBinding{StackKit: "cloud-kit", SpecPath: "stack-spec.bound.json"}
	if _, err := store.UpdateStackRuntime(ctx, "tenant-1", "stack-1", controlplane.RuntimeUpdate{
		Status: "running", RuntimeSummary: map[string]any{jobs.StackKitRolloutBindingResultField: binding.Map()},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertWorkerHeartbeat(ctx, controlplane.Worker{
		ID: "worker-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", Hostname: "cloud-main",
		Approved: true, Capabilities: map[string]any{"runtime_agent_id": "agent-1", "server_id": "server-1"},
	}); err != nil {
		t.Fatal(err)
	}
	orch := orchestrator.New(&orchestrator.Config{Workers: 0, StackStore: store, JobStore: store, WorkerStore: store}, nil)
	t.Cleanup(orch.Stop)

	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	// The owner's current backup schedule is the only row the gate reads; the
	// feature store answers nothing, so the entitlement is not held.
	mock.ExpectBegin()
	mock.ExpectExec("set_config").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("stack_backup_schedules").WillReturnRows(
		sqlmock.NewRows([]string{"tenant_id", "stack_id", "owner_id", "include_content", "enabled"}).
			AddRow("tenant-1", "stack-1", "owner-1", false, true))
	mock.ExpectCommit()

	encryptor, err := storageauth.NewSecretEncryptor([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	deps := routeDeps{orch: orch, v2: &v2Boot{db: &db.DB{DB: sqlDB}}, log: logger.NewNop()}
	composeBackupScanner(&deps, encryptor)

	_, err = orch.EnqueueStackKitLifecycle(ctx, jobs.StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1",
		Operation: jobs.StackKitLifecycleRestoreDrill, OwnerApproved: true,
	})
	var denied *orchestrator.ManagedBackupDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("restore drill result = %v, want the entitlement gate's structured denial", err)
	}
}
