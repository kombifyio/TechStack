package providercontroljobs

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/localdb"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// neverEnrolledRecoverySchemaStub recreates only the columns the candidate
// function reads. The real reservation guards require a coherent native
// admission, which is not what this selection test is about; the function
// definition itself is loaded from the versioned migration so the test cannot
// drift from the shipped SQL.
const neverEnrolledRecoverySchemaStub = `
CREATE TABLE techstack_vm_leases (
    id text NOT NULL,
    tenant_id text NOT NULL,
    server_id text NOT NULL,
    resource_generation_id uuid NOT NULL,
    desired_state text NOT NULL,
    cancelled_at timestamptz,
    lease_json jsonb NOT NULL,
    PRIMARY KEY (tenant_id, id)
);
CREATE TABLE runtime_lease_execution_authorities (
    tenant_id text NOT NULL,
    lease_id text NOT NULL,
    execution_authority text NOT NULL
);
CREATE TABLE managed_runtime_capacity_reservations (
    tenant_id text NOT NULL,
    owner_subject_id text NOT NULL,
    lease_id text NOT NULL,
    resource_generation_id uuid NOT NULL,
    reserved_at timestamptz NOT NULL
);
CREATE TABLE managed_runtime_capacity_release_facts (
    tenant_id text NOT NULL,
    lease_id text NOT NULL,
    resource_generation_id uuid NOT NULL
);
CREATE TABLE servers (
    id text NOT NULL,
    tenant_id text NOT NULL,
    lease_id text NOT NULL,
    worker_id text,
    last_heartbeat_at timestamptz,
    lifecycle_state text NOT NULL,
    connection_state text NOT NULL,
    desired_state text NOT NULL,
    decommissioned_at timestamptz
);
CREATE TABLE jobs (
    tenant_id text NOT NULL,
    stack_id text NOT NULL,
    state text NOT NULL
);
`

type recordingReaperDecommissioner struct {
	leaseIDs []string
}

func (r *recordingReaperDecommissioner) DecommissionManagedLeases(_ context.Context, req jobs.ManagedLeaseDecommissionRequest) (*jobs.ManagedLeaseDecommissionResult, error) {
	r.leaseIDs = append(r.leaseIDs, req.LeaseID)
	return &jobs.ManagedLeaseDecommissionResult{LeaseIDs: []string{req.LeaseID}, Decommissioned: 1}, nil
}

func openNeverEnrolledRecoveryDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("TECHSTACK_PROVIDERCONTROL_EMBEDDED_POSTGRES")) != "1" {
			t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; set TECHSTACK_PROVIDERCONTROL_EMBEDDED_POSTGRES=1 for the isolated never-enrolled recovery regression")
		}
		embedded, err := localdb.StartEmbeddedPostgres(t.TempDir())
		if err != nil {
			t.Fatalf("start isolated embedded PostgreSQL: %v", err)
		}
		t.Cleanup(func() {
			if stopErr := embedded.Stop(); stopErr != nil {
				t.Errorf("stop isolated embedded PostgreSQL: %v", stopErr)
			}
		})
		dsn = embedded.DSN()
	}
	adminConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse PostgreSQL DSN: %v", err)
	}
	admin := stdlib.OpenDB(*adminConfig)
	if err := admin.PingContext(t.Context()); err != nil {
		_ = admin.Close()
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	schema := fmt.Sprintf("never_enrolled_recovery_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.ExecContext(t.Context(), "CREATE SCHEMA "+quotedSchema); err != nil {
		_ = admin.Close()
		t.Fatalf("create isolated PostgreSQL schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		_ = admin.Close()
	})
	scopedConfig := adminConfig.Copy()
	scopedConfig.RuntimeParams = make(map[string]string, len(adminConfig.RuntimeParams)+1)
	for key, value := range adminConfig.RuntimeParams {
		scopedConfig.RuntimeParams[key] = value
	}
	scopedConfig.RuntimeParams["search_path"] = schema + ",public"
	database := stdlib.OpenDB(*scopedConfig)
	database.SetMaxOpenConns(4)
	database.SetMaxIdleConns(4)
	if err := database.PingContext(t.Context()); err != nil {
		_ = database.Close()
		t.Fatalf("open scoped PostgreSQL database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.ExecContext(t.Context(), neverEnrolledRecoverySchemaStub); err != nil {
		t.Fatalf("create candidate schema stubs: %v", err)
	}
	migrationSQL, err := os.ReadFile(filepath.Join("..", "..", "pkg", "db", "migrations", "114_never_enrolled_runtime_recovery.sql"))
	if err != nil {
		t.Fatalf("read candidate migration: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), string(migrationSQL)); err != nil {
		t.Fatalf("apply candidate migration: %v", err)
	}
	// The shipped function pins search_path to public, where its tables live in
	// production. The isolated schema must be resolvable instead.
	if _, err := database.ExecContext(t.Context(), fmt.Sprintf(`
		ALTER FUNCTION provider_control_list_never_enrolled_runtime_candidates(text, text, integer, integer)
			SET search_path TO %s, pg_catalog, pg_temp
	`, quotedSchema)); err != nil {
		t.Fatalf("scope candidate function search_path: %v", err)
	}
	return database
}

// TestIntegrationNeverEnrolledRecoverySelectsOnlyUnenrolled proves the
// reaper's selection contract against the real PostgreSQL candidate function:
// a reserved lease past its enrollment window with no enrolled runtime is
// decommissioned through the existing decommissioner, while enrolled, fresh,
// absence-requested, and actively-worked leases are skipped. No provider call
// is made; the decommissioner is a recorder.
func TestIntegrationNeverEnrolledRecoverySelectsOnlyUnenrolled(t *testing.T) {
	database := openNeverEnrolledRecoveryDB(t)
	ctx := context.Background()
	const (
		tenantID = "tenant-never-enrolled"
		ownerID  = "owner-never-enrolled"
	)
	seedLease := func(label, stackID string, reservedAgo time.Duration, desiredState string) (string, string) {
		t.Helper()
		leaseID := "lease-" + label
		serverID := "server-" + label
		generation := "11111111-2222-4333-8444-5555555555" + map[string]string{"reap": "01", "enrolled": "02", "fresh": "03", "worked": "04", "absent": "05"}[label]
		if _, err := database.ExecContext(ctx, `
			INSERT INTO techstack_vm_leases (id, tenant_id, server_id, resource_generation_id, desired_state, cancelled_at, lease_json)
			VALUES ($1, $2, $3, $4::uuid, $5, NULL, jsonb_build_object('metadata', jsonb_build_object('stack_id', $6::text)))
		`, leaseID, tenantID, serverID, generation, desiredState, stackID); err != nil {
			t.Fatalf("seed lease %s: %v", label, err)
		}
		if _, err := database.ExecContext(ctx, `
			INSERT INTO runtime_lease_execution_authorities (tenant_id, lease_id, execution_authority)
			VALUES ($1, $2, 'techstack_provider_control')
		`, tenantID, leaseID); err != nil {
			t.Fatalf("seed authority %s: %v", label, err)
		}
		if _, err := database.ExecContext(ctx, `
			INSERT INTO managed_runtime_capacity_reservations (tenant_id, owner_subject_id, lease_id, resource_generation_id, reserved_at)
			VALUES ($1, $2, $3, $4::uuid, $5)
		`, tenantID, ownerID, leaseID, generation, time.Now().UTC().Add(-reservedAgo)); err != nil {
			t.Fatalf("seed reservation %s: %v", label, err)
		}
		return leaseID, serverID
	}

	reapLease, _ := seedLease("reap", "stack-reap", 2*time.Hour, "running")

	enrolledLease, enrolledServer := seedLease("enrolled", "stack-enrolled", 2*time.Hour, "running")
	if _, err := database.ExecContext(ctx, `
		INSERT INTO servers (id, tenant_id, lease_id, worker_id, last_heartbeat_at, lifecycle_state, connection_state, desired_state)
		VALUES ($1, $2, $3, 'worker-enrolled', clock_timestamp(), 'active', 'connected', 'active')
	`, enrolledServer, tenantID, enrolledLease); err != nil {
		t.Fatalf("seed enrolled server: %v", err)
	}

	freshLease, _ := seedLease("fresh", "stack-fresh", time.Minute, "running")

	workedLease, _ := seedLease("worked", "stack-worked", 2*time.Hour, "running")
	if _, err := database.ExecContext(ctx, `
		INSERT INTO jobs (tenant_id, stack_id, state) VALUES ($1, 'stack-worked', 'pending')
	`, tenantID); err != nil {
		t.Fatalf("seed active job: %v", err)
	}

	absentLease, _ := seedLease("absent", "stack-absent", 2*time.Hour, "absent")

	recorder := &recordingReaperDecommissioner{}
	reaped := map[string]bool{}
	worker, err := NewNeverEnrolledRecovery(NeverEnrolledRecoveryConfig{
		Database: database, Decommissioner: recorder,
		EnrollmentWindow: MinimumNeverEnrolledRecoveryWindow,
		BatchSize:        101,
		OnReap: func(candidate NeverEnrolledRecoveryCandidate) {
			reaped[candidate.LeaseID] = true
		},
	})
	if err != nil {
		t.Fatalf("NewNeverEnrolledRecovery: %v", err)
	}
	if err := worker.RecoverOnce(ctx); err != nil {
		t.Fatalf("RecoverOnce: %v", err)
	}

	if !reaped[reapLease] {
		t.Fatalf("never-enrolled lease was not selected: reaped=%v", reaped)
	}
	decommissioned := false
	for _, leaseID := range recorder.leaseIDs {
		if leaseID == reapLease {
			decommissioned = true
		}
	}
	if !decommissioned {
		t.Fatalf("never-enrolled lease did not reach the existing decommissioner: %v", recorder.leaseIDs)
	}
	for label, leaseID := range map[string]string{
		"enrolled": enrolledLease,
		"fresh":    freshLease,
		"worked":   workedLease,
		"absent":   absentLease,
	} {
		if reaped[leaseID] {
			t.Fatalf("%s lease %s must not be reaped", label, leaseID)
		}
		for _, candidate := range recorder.leaseIDs {
			if candidate == leaseID {
				t.Fatalf("%s lease %s must not reach the decommissioner", label, leaseID)
			}
		}
	}
}
