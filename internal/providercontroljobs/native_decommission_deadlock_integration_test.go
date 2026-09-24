package providercontroljobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/localdb"
	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/vmleases"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// TestNativeDecommissionReturnsRetryablePostgresDeadlock exercises the real
// PostgreSQL candidate transaction. The trigger is scoped to this generated
// schema and emits the exact SQLSTATE from the IONOS incident before any
// provider operation is started.
func TestNativeDecommissionReturnsRetryablePostgresDeadlock(t *testing.T) {
	database := openNativeDecommissionDeadlockDB(t)
	const (
		tenantID   = "tenant-deadlock"
		ownerID    = "owner-deadlock"
		stackID    = "stack-deadlock"
		leaseID    = "lease-deadlock"
		serverID   = "server-deadlock"
		generation = "11111111-2222-4333-8444-555555555555"
	)
	lease := vmlease.Lease{
		ID:             leaseID,
		Subject:        vmlease.Subject{Kind: vmlease.SubjectUser, ID: ownerID, OrgID: tenantID},
		Resource:       vmlease.ResourceRef{ProviderID: "ionos", VMID: "provider-server-deadlock"},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		Metadata: map[string]string{
			vmleases.MetadataKeyResourceGenerationID: generation,
			"stack_id":                               stackID,
		},
	}
	leaseJSON, err := json.Marshal(lease)
	if err != nil {
		t.Fatalf("marshal native lease: %v", err)
	}
	seedTx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin native decommission authority seed: %v", err)
	}
	defer func() { _ = seedTx.Rollback() }()
	if _, err := seedTx.ExecContext(t.Context(), `
		INSERT INTO servers (
			tenant_id, id, lease_id, revision, generation, lifecycle_state, desired_state
		) VALUES ($1, $2, $3, 1, 1, 'active', 'running')
	`, tenantID, serverID, leaseID); err != nil {
		t.Fatalf("seed native decommission server: %v", err)
	}
	if _, err := seedTx.ExecContext(t.Context(), `
		INSERT INTO techstack_vm_leases (
			tenant_id, id, lease_revision, server_id, resource_generation_id,
			provider_id, owner_subject_id, lease_json, valid_until, desired_state
		) VALUES (
			$1, $2, 1, $3, $4::uuid, 'ionos', $5, $6::jsonb,
			clock_timestamp() + interval '1 hour', 'running'
		)
	`, tenantID, leaseID, serverID, generation, ownerID, string(leaseJSON)); err != nil {
		t.Fatalf("seed native decommission lease: %v", err)
	}
	if _, err := seedTx.ExecContext(t.Context(), `
		INSERT INTO runtime_lease_execution_authorities (
			tenant_id, lease_id, execution_authority
		) VALUES ($1, $2, 'techstack_provider_control')
	`, tenantID, leaseID); err != nil {
		t.Fatalf("seed native decommission execution authority: %v", err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatalf("commit native decommission authority seed: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), `
		CREATE OR REPLACE FUNCTION native_decommission_deadlock_test()
		RETURNS trigger
		LANGUAGE plpgsql
		AS $$
		BEGIN
			RAISE EXCEPTION 'simulated native decommission deadlock'
				USING ERRCODE = '40P01';
		END;
		$$;
		CREATE TRIGGER native_decommission_deadlock_test
		BEFORE UPDATE OF desired_state, cancelled_at ON techstack_vm_leases
		FOR EACH ROW EXECUTE FUNCTION native_decommission_deadlock_test();
	`); err != nil {
		t.Fatalf("install isolated SQLSTATE trigger: %v", err)
	}

	decommissioner, err := NewNativeDecommissioner(NativeDecommissionConfig{
		Database: database, Application: deadlockTestApplication{}, Ledger: &providercontrol.PostgresLedger{},
		ProvisionResolution: unavailableProvisionResolution{}, ResolutionSubject: "test:provider-control-worker",
	})
	if err != nil {
		t.Fatalf("NewNativeDecommissioner: %v", err)
	}
	result, err := decommissioner.DecommissionManagedLeases(t.Context(), jobs.ManagedLeaseDecommissionRequest{
		TenantID: tenantID, OwnerID: ownerID, StackID: stackID, LeaseID: leaseID,
	})
	if result != nil {
		t.Fatalf("deadlock result = %#v, want no partial result before candidate preparation commits", result)
	}
	var wait *jobs.JobWaitError
	if !errors.As(err, &wait) {
		t.Fatalf("decommission error = %v, want retryable JobWaitError for PostgreSQL SQLSTATE 40P01", err)
	}
	if wait.Reason != nativeDecommissionWaitReason {
		t.Fatalf("deadlock wait reason = %q, want %q", wait.Reason, nativeDecommissionWaitReason)
	}
}

func TestNativeDecommissionDeadlockWaitRejectsOtherPostgresFailures(t *testing.T) {
	deadlockDetail := "raw provider credential detail must not reach the job UI"
	retry := nativeDecommissionDeadlockWait(fmt.Errorf("wrapped: %w", &pgconn.PgError{
		Code: "40P01", Message: deadlockDetail,
	}))
	if retry == nil {
		t.Fatal("PostgreSQL deadlock did not produce a resumable cleanup wait")
	}
	if retry.Error() != "Managed server cleanup is retrying after a concurrent control-plane transaction." ||
		strings.Contains(retry.Error(), deadlockDetail) {
		t.Fatalf("deadlock wait exposed database detail: %q", retry.Error())
	}
	if retry := nativeDecommissionDeadlockWait(fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "40001"})); retry != nil {
		t.Fatalf("serialization failure converted to cleanup wait = %#v", retry)
	}
	if retry := nativeDecommissionDeadlockWait(errors.New("connection reset")); retry != nil {
		t.Fatalf("non-PostgreSQL failure converted to cleanup wait = %#v", retry)
	}
}

type deadlockTestApplication struct{}

func (deadlockTestApplication) Start(context.Context, providercontrol.StartRequest) (providercontrol.OperationRecord, bool, error) {
	return providercontrol.OperationRecord{}, false, errors.New("deadlock retry must not start a provider operation")
}

func (deadlockTestApplication) Get(context.Context, string, string) (providercontrol.OperationRecord, error) {
	return providercontrol.OperationRecord{}, errors.New("deadlock retry must not load a provider operation")
}

func (deadlockTestApplication) Advance(context.Context, string, string) (providercontrol.OperationRecord, bool, error) {
	return providercontrol.OperationRecord{}, false, errors.New("deadlock retry must not advance a provider operation")
}

func openNativeDecommissionDeadlockDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL"))
	if dsn == "" {
		if strings.TrimSpace(os.Getenv("TECHSTACK_PROVIDERCONTROL_EMBEDDED_POSTGRES")) != "1" {
			t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; set TECHSTACK_PROVIDERCONTROL_EMBEDDED_POSTGRES=1 for the isolated PostgreSQL deadlock regression")
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
	schema := fmt.Sprintf("native_decommission_deadlock_%d", time.Now().UnixNano())
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
	if _, err := database.ExecContext(t.Context(), `
		CREATE TABLE servers (
			tenant_id text NOT NULL,
			id text NOT NULL,
			lease_id text NOT NULL,
			revision bigint NOT NULL,
			generation bigint NOT NULL,
			lifecycle_state text NOT NULL,
			desired_state text NOT NULL,
			PRIMARY KEY (tenant_id, id)
		);
		CREATE TABLE techstack_vm_leases (
			tenant_id text NOT NULL,
			id text NOT NULL,
			lease_revision bigint NOT NULL,
			server_id text NOT NULL,
			resource_generation_id uuid NOT NULL,
			provider_id text NOT NULL,
			owner_subject_id text NOT NULL,
			lease_json jsonb NOT NULL,
			cancelled_at timestamptz,
			valid_until timestamptz NOT NULL,
			desired_state text NOT NULL,
			updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
			PRIMARY KEY (tenant_id, id)
		);
		CREATE TABLE runtime_lease_execution_authorities (
			tenant_id text NOT NULL,
			lease_id text NOT NULL,
			execution_authority text NOT NULL,
			PRIMARY KEY (tenant_id, lease_id)
		);
	`); err != nil {
		t.Fatalf("create narrow native decommission schema: %v", err)
	}
	return database
}
