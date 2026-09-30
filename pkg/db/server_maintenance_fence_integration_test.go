package db_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	pgkdb "github.com/kombifyio/techstack/pkg/db"
)

// The per-server maintenance fence is a database invariant, not an in-process
// lock: a second active reboot or OS update for one server fails the insert on
// every replica, while an update plan holds its own slot.
func TestIntegrationServerMaintenanceFenceAdmitsOneMutationPerServer(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL"))
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping Postgres integration test")
	}
	database, err := pgkdb.Open(pgkdb.Config{Backend: pgkdb.StoreBackendPostgres, DSN: dsn, DriverName: pgkdb.PostgresDriverName})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	store := controlplane.NewPostgresStore(database.DB)
	const tenantID = "tenant-maintenance-fence"
	if _, err := store.EnsureTenant(t.Context(), controlplane.Tenant{ID: tenantID, DisplayName: tenantID, Kind: "self_hosted", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().UTC().Add(15 * time.Minute).Truncate(time.Microsecond)
	create := func(id, action string) error {
		created, err := store.CreateServerMaintenanceJob(t.Context(), controlplane.ServerMaintenanceJob{
			ID: id, TenantID: tenantID, ServerID: "server-fence", AgentID: "agent-fence", OwnerSubjectID: "owner-fence",
			Action: action, RequestDigest: "digest-" + id, State: controlplane.ServerMaintenanceStateQueued, InventoryRevision: 1,
			DeadlineAt: &deadline,
		})
		if err == nil && (created.DeadlineAt == nil || !created.DeadlineAt.Equal(deadline)) {
			t.Fatalf("created deadline = %v, want %v", created.DeadlineAt, deadline)
		}
		return err
	}
	if err := create("plan-1", controlplane.ServerMaintenanceActionPlan); err != nil {
		t.Fatalf("plan: %v", err)
	}
	if err := create("reboot-1", controlplane.ServerMaintenanceActionReboot); err != nil {
		t.Fatalf("reboot next to a plan: %v", err)
	}
	if err := create("update-1", controlplane.ServerMaintenanceActionUpdate); !errors.Is(err, controlplane.ErrServerMaintenanceActive) {
		t.Fatalf("second mutation err = %v, want ErrServerMaintenanceActive", err)
	}
	if err := create("reboot-1", controlplane.ServerMaintenanceActionReboot); !errors.Is(err, controlplane.ErrConflict) {
		t.Fatalf("same id err = %v, want ErrConflict", err)
	}
	if _, err := store.UpdateServerMaintenanceJob(t.Context(), tenantID, "reboot-1", controlplane.ServerMaintenanceUpdate{
		ExpectedState: controlplane.ServerMaintenanceStateQueued, State: controlplane.ServerMaintenanceStateFailed, ReasonCode: "node_did_not_return",
	}); err != nil {
		t.Fatalf("finish reboot: %v", err)
	}
	if err := create("update-2", controlplane.ServerMaintenanceActionUpdate); err != nil {
		t.Fatalf("mutation after the first finished: %v", err)
	}
	active, err := store.ActiveServerMaintenanceJob(t.Context(), tenantID, controlplane.ServerMaintenanceScope{AgentID: "agent-fence"})
	if err != nil || active.ID != "update-2" {
		t.Fatalf("active by agent = %+v, %v; want update-2", active, err)
	}
}

// Node admission is one transaction on one connection: it runs even on a
// single-connection pool (a pinned lock connection plus a second one for the
// check would hang there), refuses a node with pending stack or service work
// by the agent in the job payload, and the runner's claim applies the same
// check.
func TestIntegrationNodeAdmissionUsesOneConnection(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL"))
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping Postgres integration test")
	}
	database, err := pgkdb.Open(pgkdb.Config{Backend: pgkdb.StoreBackendPostgres, DSN: dsn, DriverName: pgkdb.PostgresDriverName})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	store := controlplane.NewPostgresStore(database.DB)
	const tenantID = "tenant-node-admission"
	if _, err := store.EnsureTenant(t.Context(), controlplane.Tenant{ID: tenantID, DisplayName: tenantID, Kind: "self_hosted", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	database.DB.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	job := func(id string) controlplane.ServerMaintenanceJob {
		return controlplane.ServerMaintenanceJob{
			ID: id, TenantID: tenantID, ServerID: "server-admission", AgentID: "agent-admission", OwnerSubjectID: "owner-admission",
			Action: controlplane.ServerMaintenanceActionReboot, RequestDigest: "digest-" + id,
			State: controlplane.ServerMaintenanceStateQueued, InventoryRevision: 1,
		}
	}
	if _, err := store.AdmitServerMaintenanceJob(ctx, job("reboot-free")); err != nil {
		t.Fatalf("admission on a free node: %v", err)
	}
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-node-restart", TenantID: tenantID, Type: "restart", State: "pending",
		Payload: map[string]any{"agent_id": "agent-admission"},
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var busy *controlplane.NodeBusyError
	if _, err := store.ClaimServerMaintenanceJob(ctx, tenantID, "reboot-free", "cmd-reboot-free", now.Add(5*time.Minute), now); !errors.As(err, &busy) || busy.JobID != "job-node-restart" {
		t.Fatalf("claim on a busy node err = %v, want NodeBusyError(job-node-restart)", err)
	}
	if _, err := store.UpdateServerMaintenanceJob(ctx, tenantID, "reboot-free", controlplane.ServerMaintenanceUpdate{
		ExpectedState: controlplane.ServerMaintenanceStateQueued, State: controlplane.ServerMaintenanceStateCancelled,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitServerMaintenanceJob(ctx, job("reboot-busy")); !errors.As(err, &busy) || busy.JobID != "job-node-restart" {
		t.Fatalf("admission on a busy node err = %v, want NodeBusyError(job-node-restart)", err)
	}
	if _, err := store.UpsertJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-node-restart", TenantID: tenantID, Type: "restart", State: "completed",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitServerMaintenanceJob(ctx, job("reboot-after")); err != nil {
		t.Fatalf("admission after the node freed up: %v", err)
	}
	claimed, err := store.ClaimServerMaintenanceJob(ctx, tenantID, "reboot-after", "cmd-reboot-after", now.Add(5*time.Minute), now)
	if err != nil || claimed.State != controlplane.ServerMaintenanceStateRunning || claimed.DeadlineAt == nil {
		t.Fatalf("claim on a free node = %+v, %v; want running with a deadline", claimed, err)
	}
}

// The runner finds active maintenance across tenants through the wake-up
// directory the insert trigger fills, retires the entry once the tenant has
// none left, and StartJob defers a stack job while a claimed reboot holds its
// agent.
func TestIntegrationServerMaintenanceRunnerDirectoryAndStackJobDefer(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL"))
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping Postgres integration test")
	}
	database, err := pgkdb.Open(pgkdb.Config{Backend: pgkdb.StoreBackendPostgres, DSN: dsn, DriverName: pgkdb.PostgresDriverName})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	store := controlplane.NewPostgresStore(database.DB)
	const tenantID = "tenant-maintenance-runner"
	if _, err := store.EnsureTenant(t.Context(), controlplane.Tenant{ID: tenantID, DisplayName: tenantID, Kind: "self_hosted", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	ctx, now := t.Context(), time.Now().UTC()
	if _, err := store.AdmitServerMaintenanceJob(ctx, controlplane.ServerMaintenanceJob{
		ID: "reboot-runner", TenantID: tenantID, ServerID: "server-runner", AgentID: "agent-runner", OwnerSubjectID: "owner-runner",
		Action: controlplane.ServerMaintenanceActionReboot, RequestDigest: "digest-runner", State: controlplane.ServerMaintenanceStateQueued, InventoryRevision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	tenants, err := store.ListServerMaintenanceTenants(ctx, "", 100)
	if err != nil || !slices.Contains(tenants, tenantID) {
		t.Fatalf("directory = %v, %v; want %s", tenants, err, tenantID)
	}
	if active, err := store.ListActiveServerMaintenanceJobs(ctx, tenantID, 10); err != nil || len(active) != 1 || active[0].ID != "reboot-runner" {
		t.Fatalf("active jobs = %+v, %v; want reboot-runner", active, err)
	}
	if _, err := store.ClaimServerMaintenanceJob(ctx, tenantID, "reboot-runner", "cmd-reboot-runner", now.Add(5*time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-runner-restart", TenantID: tenantID, Type: "restart", State: "pending", Payload: map[string]any{"agent_id": "agent-runner"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartJob(ctx, tenantID, "job-runner-restart", now); !errors.Is(err, controlplane.ErrNodeUnderMaintenance) {
		t.Fatalf("start under a claimed reboot err = %v, want ErrNodeUnderMaintenance", err)
	}
	if _, err := store.UpdateServerMaintenanceJob(ctx, tenantID, "reboot-runner", controlplane.ServerMaintenanceUpdate{
		ExpectedState: controlplane.ServerMaintenanceStateRunning, State: controlplane.ServerMaintenanceStateCompleted, At: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartJob(ctx, tenantID, "job-runner-restart", now); err != nil {
		t.Fatalf("start after the reboot settled: %v", err)
	}
	if _, err := database.DB.ExecContext(ctx, `UPDATE server_maintenance_tenants SET refreshed_at = now() - interval '2 minutes' WHERE tenant_id = $1`, tenantID); err != nil {
		t.Fatal(err)
	}
	if err := store.CompactServerMaintenanceTenant(ctx, tenantID); err != nil {
		t.Fatal(err)
	}
	if tenants, err := store.ListServerMaintenanceTenants(ctx, "", 100); err != nil || slices.Contains(tenants, tenantID) {
		t.Fatalf("directory after compaction = %v, %v; want %s retired", tenants, err, tenantID)
	}
}
