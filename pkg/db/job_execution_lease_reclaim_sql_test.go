package db

import (
	"strings"
	"testing"
)

// TestJobExecutionLeaseReclaimMigrationPosture locks the shape of the reclaim
// schema: an explicit lease on the job row, and a wake-up directory that may
// carry tenant IDs and one scheduling timestamp cross-tenant while every job
// payload read stays behind the tenant-scoped FORCE RLS boundary.
func TestJobExecutionLeaseReclaimMigrationPosture(t *testing.T) {
	content, err := migrationsFS.ReadFile("migrations/075_job_execution_lease_reclaim.sql")
	if err != nil {
		t.Fatal(err)
	}
	source := string(content)
	for _, required := range []string{
		"ADD COLUMN IF NOT EXISTS execution_owner_id text",
		"ADD COLUMN IF NOT EXISTS execution_lease_expires_at timestamptz",
		"CREATE INDEX IF NOT EXISTS idx_jobs_execution_lease_expiry",
		"CREATE TABLE IF NOT EXISTS job_execution_reclaim_tenants",
		"REVOKE ALL ON TABLE job_execution_reclaim_tenants FROM PUBLIC",
		"CREATE OR REPLACE FUNCTION jobs_wake_execution_reclaim_tenant()",
		"SECURITY INVOKER",
		"WHEN (NEW.state = 'running' AND NEW.execution_lease_expires_at IS NOT NULL)",
		"ALTER TABLE jobs NO FORCE ROW LEVEL SECURITY",
		"ALTER TABLE jobs FORCE ROW LEVEL SECURITY",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("migration missing posture invariant %q", required)
		}
	}
	if strings.Contains(source, "SECURITY DEFINER") {
		t.Fatal("the reclaim wake-up directory must not need definer rights; job reads stay tenant-scoped")
	}
	// Historical debris is the reason this migration exists: rows stranded
	// 'running' by a dead process must come out of it already reclaimable.
	if !strings.Contains(source, "SET execution_lease_expires_at = updated_at + interval '90 seconds'") {
		t.Fatal("migration must backfill a lease for rows already stranded in 'running'")
	}
	// The backfill is cross-tenant, so it must not fire the per-row triggers
	// that require an exact tenant scope (071 raises 42501) or bump updated_at.
	backfillStart := strings.Index(source, "ALTER TABLE jobs DISABLE TRIGGER USER")
	backfillEnd := strings.Index(source, "ALTER TABLE jobs ENABLE TRIGGER USER")
	updateAt := strings.Index(source, "SET execution_lease_expires_at = updated_at")
	if backfillStart < 0 || backfillEnd < backfillStart || updateAt < backfillStart || updateAt > backfillEnd {
		t.Fatal("the cross-tenant backfill must run with the jobs user triggers disabled")
	}
	// The new wake trigger must be created after the backfill window, so the
	// directory is seeded by the explicit INSERT rather than by row triggers.
	if createTrigger := strings.Index(source, "CREATE TRIGGER jobs_wake_execution_reclaim"); createTrigger < backfillEnd {
		t.Fatal("the wake trigger must be created after the backfill window closes")
	}
	for _, forbidden := range []string{"payload_json", "logs_json", "error_details"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("the reclaim directory migration must stay secret-free, found %q", forbidden)
		}
	}
}
