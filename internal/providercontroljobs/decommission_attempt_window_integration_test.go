package providercontroljobs

import (
	"testing"
)

// TestIntegrationDecommissionAttemptBudgetReArmsAfterTheWindow pins the live
// failure on lease-5a45844e: four decommission attempts failed on 2026-08-31
// against an adapter defect and the budget never re-armed, so the VM billed
// on after the defect was fixed. Old failures must still number the next key
// (so it cannot replay an old verdict) but must no longer exhaust the budget.
func TestIntegrationDecommissionAttemptBudgetReArmsAfterTheWindow(t *testing.T) {
	database := openNeverEnrolledRecoveryDB(t)
	if _, err := database.ExecContext(t.Context(), `
		CREATE TABLE provider_operations (
		    tenant_id text NOT NULL,
		    lease_id text NOT NULL,
		    operation text NOT NULL,
		    status text NOT NULL,
		    idempotency_key text NOT NULL,
		    updated_at timestamptz NOT NULL
		)`); err != nil {
		t.Fatalf("create provider_operations stub: %v", err)
	}
	const (
		tenant     = "tenant-a"
		lease      = "lease-5a45844e9f5b7e1811c532bc5c1ac228"
		generation = "fb690484-7083-4127-987f-06ace1d959b1"
	)
	base := nativeDecommissionBaseIdempotencyKey(lease, generation)
	insert := func(key, status, age string) {
		t.Helper()
		if _, err := database.ExecContext(t.Context(), `
			INSERT INTO provider_operations (tenant_id, lease_id, operation, status, idempotency_key, updated_at)
			VALUES ($1, $2, 'decommission', $3, $4, now() - $5::interval)`,
			tenant, lease, status, key, age); err != nil {
			t.Fatalf("insert operation %s: %v", key, err)
		}
	}
	count := func() (int, int) {
		t.Helper()
		tx, err := database.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		total, recent, err := loadNativeDecommissionFailedAttemptsTx(t.Context(), tx, tenant, lease, generation)
		if err != nil {
			t.Fatalf("count attempts: %v", err)
		}
		return total, recent
	}

	insert(base, "failed", "22 days")
	insert(base+":retry-1", "failed", "22 days")
	insert(base+":retry-2", "failed", "22 days")
	insert(base+":retry-3", "failed", "22 days")
	total, recent := count()
	if total != maxNativeDecommissionAttempts || recent != 0 {
		t.Fatalf("aged attempts: total=%d recent=%d, want total=%d recent=0", total, recent, maxNativeDecommissionAttempts)
	}
	next := nativeDecommissionIdempotencyKey(nativeDecommissionCandidate{
		LeaseID: lease, ResourceGeneration: generation, FailedAttempts: total, RecentFailedAttempts: recent,
	})
	if next != base+":retry-4" {
		t.Fatalf("re-armed attempt key = %q; it must not replay an earlier failed operation", next)
	}

	insert(base+":retry-4", "failed", "1 minute")
	insert(base+":retry-5", "failed", "1 minute")
	insert(base+":retry-6", "failed", "1 minute")
	insert(base+":retry-7", "failed", "1 minute")
	if _, recent = count(); recent != maxNativeDecommissionAttempts {
		t.Fatalf("recent failures = %d, want the budget of %d exhausted again inside the window", recent, maxNativeDecommissionAttempts)
	}
}
