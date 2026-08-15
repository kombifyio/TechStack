package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/google/uuid"
)

type reclaimFixture struct {
	store    *controlplane.PostgresStore
	database *DB
	tenantID string
	stackID  string
	orphanID string
	followID string
	started  time.Time
}

// newIntegrationReclaimFixture reproduces the live deadlock against real
// PostgreSQL: one job holding the per-stack execution claim while a second one
// waits behind it.
func newIntegrationReclaimFixture(t *testing.T) reclaimFixture {
	t.Helper()
	database := openTestDB(t)
	ctx := context.Background()
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	suffix := uuid.NewString()
	fixture := reclaimFixture{
		store:    controlplane.NewPostgresStore(database.DB),
		database: database,
		tenantID: "reclaim-tenant-" + suffix,
		stackID:  "reclaim-stack-" + suffix,
		orphanID: "reclaim-orphan-" + suffix,
		followID: "reclaim-follow-" + suffix,
	}
	if _, err := fixture.store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: fixture.stackID, TenantID: fixture.tenantID, OwnerSubjectID: "owner-1",
		Name: "Reclaim " + suffix, Status: "provisioning",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	for _, jobID := range []string{fixture.orphanID, fixture.followID} {
		if _, err := fixture.store.CreateJob(ctx, controlplane.UpsertJobRequest{
			ID: jobID, TenantID: fixture.tenantID, StackID: fixture.stackID,
			Type: "deploy", State: "pending", Step: "generate_unified",
		}); err != nil {
			t.Fatalf("CreateJob %s: %v", jobID, err)
		}
	}
	started, err := fixture.store.StartJob(ctx, fixture.tenantID, fixture.orphanID, time.Now().UTC())
	if err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	if started.StartedAt == nil {
		t.Fatal("started job carries no execution generation")
	}
	fixture.started = *started.StartedAt

	// The deadlock: while the first job is running, nothing else on this stack
	// can start.
	if _, err := fixture.store.StartJob(ctx, fixture.tenantID, fixture.followID, time.Now().UTC()); !errors.Is(err, controlplane.ErrStackExecutionBusy) {
		t.Fatalf("follow-up StartJob error = %v, want ErrStackExecutionBusy", err)
	}
	return fixture
}

// expireLease backdates the durable lease the way 90 seconds of missed
// heartbeats would.
func (f reclaimFixture) expireLease(t *testing.T, ownerID string, expiredAt time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := f.database.WithTenant(ctx, f.tenantID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE jobs SET execution_owner_id = $3, execution_lease_expires_at = $4
			WHERE tenant_id = $1 AND id = $2
		`, f.tenantID, f.orphanID, ownerID, expiredAt.UTC())
		return err
	}); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
}

// TestIntegrationOrphanedRunningJobIsReclaimableAndUnblocksItsStack is the live
// failure end to end: a job stranded 'running' by a dead process holds its
// stack forever, and reclaiming it must be the thing that frees the lane.
func TestIntegrationOrphanedRunningJobIsReclaimableAndUnblocksItsStack(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationReclaimFixture(t)
	deadOwner := "boot-" + uuid.NewString()
	now := time.Now().UTC()
	fixture.expireLease(t, deadOwner, now.Add(-time.Minute))

	// The trigger put this tenant into the secret-free wake-up directory when
	// StartJob issued the lease, so cross-tenant discovery finds it without any
	// broad scan over jobs.
	tenants, err := fixture.store.ListJobExecutionReclaimTenants(ctx, "", 100, now)
	if err != nil {
		t.Fatalf("ListJobExecutionReclaimTenants: %v", err)
	}
	if !containsTenant(tenants, fixture.tenantID) {
		t.Fatalf("wake-up directory did not surface the stranded tenant: %v", tenants)
	}

	expired, err := fixture.store.ListExpiredJobExecutionLeases(ctx, fixture.tenantID, now, 50)
	if err != nil {
		t.Fatalf("ListExpiredJobExecutionLeases: %v", err)
	}
	if len(expired) != 1 || expired[0].JobID != fixture.orphanID {
		t.Fatalf("expired leases = %#v, want exactly the orphan", expired)
	}
	if expired[0].OwnerID != deadOwner || expired[0].StackID != fixture.stackID {
		t.Fatalf("expired lease identity = %#v", expired[0])
	}

	failed, err := fixture.store.ReclaimExpiredJobExecution(ctx, controlplane.ReclaimExpiredJobExecutionRequest{
		TenantID: fixture.tenantID, JobID: fixture.orphanID, StackID: fixture.stackID,
		ExpectedOwnerID: deadOwner, LeaseExpiredBefore: now,
		Error:        "Orphaned by process restart",
		ErrorDetails: "No progress and the execution lease expired.",
		ResultPatch: map[string]any{
			"job_execution_reclaim": map[string]any{"reason_code": "orphaned_by_process_restart"},
		},
		ReclaimedAt: now,
	})
	if err != nil {
		t.Fatalf("ReclaimExpiredJobExecution: %v", err)
	}
	if failed.State != "failed" || failed.Error == "" {
		t.Fatalf("reclaimed job = %#v, want a failed row with a durable reason", failed)
	}
	receipt, ok := failed.Result["job_execution_reclaim"].(map[string]any)
	if !ok || receipt["reason_code"] != "orphaned_by_process_restart" {
		t.Fatalf("durable receipt = %#v", failed.Result)
	}

	// The claim is released, so the job that was deferring can finally start.
	if _, err := fixture.store.StartJob(ctx, fixture.tenantID, fixture.followID, time.Now().UTC()); err != nil {
		t.Fatalf("follow-up StartJob after reclaim: %v, want the stack lane to be free", err)
	}
}

// A reclaim carrying a stale observation must lose. This is the whole
// multi-instance safety argument, exercised against the real conditional
// UPDATE rather than an in-memory approximation.
func TestIntegrationReclaimRefusesStaleObservations(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationReclaimFixture(t)
	deadOwner := "boot-" + uuid.NewString()
	now := time.Now().UTC()
	fixture.expireLease(t, deadOwner, now.Add(-time.Minute))

	for name, req := range map[string]controlplane.ReclaimExpiredJobExecutionRequest{
		"owner moved on": {
			ExpectedOwnerID: "boot-" + uuid.NewString(), LeaseExpiredBefore: now,
		},
		"lease not yet expired": {
			ExpectedOwnerID: deadOwner, LeaseExpiredBefore: now.Add(-time.Hour),
		},
		"wrong stack": {
			ExpectedOwnerID: deadOwner, LeaseExpiredBefore: now,
		},
	} {
		t.Run(name, func(t *testing.T) {
			req.TenantID = fixture.tenantID
			req.JobID = fixture.orphanID
			req.StackID = fixture.stackID
			if name == "wrong stack" {
				req.StackID = "some-other-stack"
			}
			req.Error = "Orphaned by process restart"
			req.ReclaimedAt = now
			if _, err := fixture.store.ReclaimExpiredJobExecution(ctx, req); !errors.Is(err, controlplane.ErrConflict) {
				t.Fatalf("error = %v, want ErrConflict", err)
			}
		})
	}

	job, err := fixture.store.GetJob(ctx, fixture.tenantID, fixture.orphanID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "running" {
		t.Fatalf("job state = %q, want the row untouched by every refused reclaim", job.State)
	}
}

// The progress heartbeat is the renewal. A live execution must fall out of the
// expired-lease scan on its own, without anything else having to protect it.
func TestIntegrationProgressHeartbeatRenewsTheExecutionLease(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationReclaimFixture(t)
	fixture.expireLease(t, controlplane.ProcessExecutionOwnerID(), time.Now().UTC().Add(-time.Minute))

	expired, err := fixture.store.ListExpiredJobExecutionLeases(ctx, fixture.tenantID, time.Now().UTC(), 50)
	if err != nil {
		t.Fatalf("ListExpiredJobExecutionLeases: %v", err)
	}
	if len(expired) != 1 {
		t.Fatalf("expected the backdated lease to be visible, got %#v", expired)
	}

	if _, err := fixture.store.SyncJobSnapshot(ctx, controlplane.SyncJobSnapshotRequest{
		Job: controlplane.UpsertJobRequest{
			ID: fixture.orphanID, TenantID: fixture.tenantID, StackID: fixture.stackID,
			Type: "deploy", State: "running", Progress: 60, Step: "generate_unified",
		},
		ObservedState: "running", AttemptStartedAt: &fixture.started,
	}); err != nil {
		t.Fatalf("progress heartbeat: %v", err)
	}

	expired, err = fixture.store.ListExpiredJobExecutionLeases(ctx, fixture.tenantID, time.Now().UTC(), 50)
	if err != nil {
		t.Fatalf("ListExpiredJobExecutionLeases after heartbeat: %v", err)
	}
	if len(expired) != 0 {
		t.Fatalf("a heartbeating execution is still listed as expired: %#v", expired)
	}
}

// A running row with no lease at all (written by a pre-migration binary) is
// fail-closed: it is never reclaimed on a guess. The migration's backfill is
// what makes that historical debris reclaimable, so the same expression is
// exercised here against a real row.
func TestIntegrationRunningRowWithoutALeaseIsFailClosedUntilBackfilled(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationReclaimFixture(t)

	// A pre-075 row: state running, no lease at all. The set_jobs_updated_at
	// trigger stamps updated_at, which is precisely the last-progress signal
	// the backfill derives the lease from.
	var lastProgressAt time.Time
	if err := fixture.database.WithTenant(ctx, fixture.tenantID, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `
			UPDATE jobs SET execution_owner_id = NULL, execution_lease_expires_at = NULL
			WHERE tenant_id = $1 AND id = $2
			RETURNING updated_at
		`, fixture.tenantID, fixture.orphanID).Scan(&lastProgressAt)
	}); err != nil {
		t.Fatalf("strip lease: %v", err)
	}

	expired, err := fixture.store.ListExpiredJobExecutionLeases(ctx, fixture.tenantID, time.Now().UTC(), 50)
	if err != nil {
		t.Fatalf("ListExpiredJobExecutionLeases: %v", err)
	}
	if len(expired) != 0 {
		t.Fatalf("a row with no lease was treated as reclaimable: %#v", expired)
	}

	// This is verbatim the migration's backfill expression.
	if err := fixture.database.WithTenant(ctx, fixture.tenantID, func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx, `
			UPDATE jobs
			SET execution_lease_expires_at = updated_at + interval '90 seconds'
			WHERE tenant_id = $1 AND state = 'running' AND execution_lease_expires_at IS NULL
		`, fixture.tenantID)
		return execErr
	}); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	// The backfilled lease is anchored on the row's own last progress, so a row
	// that reported a moment ago is still protected for the full grace window.
	stillLive, err := fixture.store.ListExpiredJobExecutionLeases(ctx, fixture.tenantID, lastProgressAt.Add(time.Second), 50)
	if err != nil {
		t.Fatalf("ListExpiredJobExecutionLeases inside the grace window: %v", err)
	}
	if len(stillLive) != 0 {
		t.Fatalf("backfill exposed a row that had just reported: %#v", stillLive)
	}

	// Once that grace window has elapsed, the historical debris is reclaimable.
	expired, err = fixture.store.ListExpiredJobExecutionLeases(ctx, fixture.tenantID, lastProgressAt.Add(91*time.Second), 50)
	if err != nil {
		t.Fatalf("ListExpiredJobExecutionLeases after backfill: %v", err)
	}
	if len(expired) != 1 || expired[0].JobID != fixture.orphanID {
		t.Fatalf("backfilled debris = %#v, want the stranded job to become reclaimable", expired)
	}
	if expired[0].OwnerID != "" {
		t.Fatalf("backfilled row invented an owner: %q", expired[0].OwnerID)
	}
	if want := lastProgressAt.Add(90 * time.Second); !expired[0].LeaseExpiresAt.Equal(want) {
		t.Fatalf("backfilled lease = %s, want %s derived from last progress", expired[0].LeaseExpiresAt, want)
	}
}

// Once nothing is leased, the tenant must leave the directory so idle tenants
// stop being swept forever.
func TestIntegrationReclaimDirectoryRetiresIdleTenants(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationReclaimFixture(t)
	now := time.Now().UTC()

	if _, err := fixture.store.FailJob(ctx, fixture.tenantID, fixture.orphanID, "done", "", now); err != nil {
		t.Fatalf("FailJob: %v", err)
	}
	if err := fixture.store.CompactJobExecutionReclaimTenant(ctx, fixture.tenantID); err != nil {
		t.Fatalf("CompactJobExecutionReclaimTenant: %v", err)
	}
	tenants, err := fixture.store.ListJobExecutionReclaimTenants(ctx, "", 100, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("ListJobExecutionReclaimTenants: %v", err)
	}
	if containsTenant(tenants, fixture.tenantID) {
		t.Fatalf("a tenant with no leased execution is still in the directory: %v", tenants)
	}
}

func containsTenant(tenants []string, want string) bool {
	for _, tenantID := range tenants {
		if tenantID == want {
			return true
		}
	}
	return false
}
