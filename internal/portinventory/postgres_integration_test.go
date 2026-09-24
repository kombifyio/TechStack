package portinventory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresAuthoritySerializesConflictingAdmissionsAndEnforcesTenantScope(t *testing.T) {
	database, adminDatabase, adminConfig, schema := openPortInventoryIntegrationDatabase(t)
	authority := NewPostgresAuthority(database)

	wildcard := postgresIntegrationAdmission("tenant-a", "server-a", 3, "stack-wildcard", "plan-wildcard", "*")
	concrete := postgresIntegrationAdmission("tenant-a", "server-a", 3, "stack-concrete", "plan-concrete", "127.0.0.1")

	start := make(chan struct{})
	results := make(chan error, 2)
	var callers sync.WaitGroup
	for _, request := range []AdmissionRequest{wildcard, concrete} {
		request := request
		callers.Add(1)
		go func() {
			defer callers.Done()
			<-start
			_, err := authority.Admit(t.Context(), request)
			results <- err
		}()
	}
	close(start)
	callers.Wait()
	close(results)

	var admitted, conflicted int
	for err := range results {
		switch {
		case err == nil:
			admitted++
		case errors.Is(err, ErrAllocationConflict):
			var conflict *ConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("conflict error = %T %v, want *ConflictError", err, err)
			}
			if conflict.Port != 443 || conflict.Transport != TransportTCP {
				t.Fatalf("conflict = %#v, want tcp/443", conflict)
			}
			conflicted++
		default:
			t.Fatalf("concurrent Admit() error = %v", err)
		}
	}
	if admitted != 1 || conflicted != 1 {
		t.Fatalf("concurrent results = admitted %d conflicted %d, want 1/1", admitted, conflicted)
	}

	assertPortInventoryRowCount(t, database, "server_port_reservations", 1)
	assertPortInventoryRowCount(t, database, "server_port_claim_generations", 1)
	assertPortInventoryRowCount(t, database, "server_port_reservation_claims", 1)

	stale := postgresIntegrationAdmission("tenant-a", "server-a", 4, "stack-stale", "plan-stale", "127.0.0.2")
	if _, err := authority.Admit(t.Context(), stale); !errors.Is(err, ErrStaleServerGeneration) {
		t.Fatalf("Admit(stale generation) error = %v, want ErrStaleServerGeneration", err)
	}

	var admittedStackID, admittedPlanHash string
	if err := database.QueryRowContext(t.Context(), `
		SELECT stack_id, resolved_plan_hash
		FROM server_port_claim_generations
		WHERE tenant_id = 'tenant-a' AND server_id = 'server-a' AND server_generation = 3
	`).Scan(&admittedStackID, &admittedPlanHash); err != nil {
		t.Fatalf("load admitted generation identity: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), `UPDATE servers SET generation = 4 WHERE tenant_id = 'tenant-a' AND id = 'server-a'`); err != nil {
		t.Fatalf("advance canonical server generation: %v", err)
	}
	if err := authority.AbortBeforeMutation(t.Context(), GenerationRef{
		ServerRef: ServerRef{TenantID: "tenant-a", ServerID: "server-a", ServerGeneration: 3},
		StackID:   admittedStackID, ResolvedPlanHash: admittedPlanHash,
	}); err != nil {
		t.Fatalf("AbortBeforeMutation(historical generation) error = %v", err)
	}
	var reservationState string
	if err := database.QueryRowContext(t.Context(), `
		SELECT state FROM server_port_reservations
		WHERE tenant_id = 'tenant-a' AND server_id = 'server-a' AND server_generation = 3
	`).Scan(&reservationState); err != nil {
		t.Fatalf("load historical reservation state: %v", err)
	}
	if reservationState != "released" {
		t.Fatalf("historical reservation state = %q, want released", reservationState)
	}

	t.Run("row level security hides another tenant inventory", func(t *testing.T) {
		restricted := openRestrictedPortInventoryDatabase(t, adminDatabase, adminConfig, schema)
		assertRestrictedTenantCount(t, restricted, "tenant-a", 1)
		assertRestrictedTenantCount(t, restricted, "tenant-b", 0)
	})
}

func TestPostgresAuthorityReleasesOnlyAnExactTeardownSnapshot(t *testing.T) {
	database, _, _, _ := openPortInventoryIntegrationDatabase(t)
	assertExactTeardownSnapshotLifecycle(t, database)
}

func TestPostgresAuthorityProjectsMonotoneGuardListenerDrift(t *testing.T) {
	database, _, _, _ := openPortInventoryIntegrationDatabase(t)
	authority := NewPostgresAuthority(database)
	request := postgresIntegrationAdmission("tenant-a", "server-a", 3, "stack-a", "plan-ports", "*")
	if _, err := authority.Admit(t.Context(), request); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	now := time.Now().UTC()
	first := GuardObservation{
		TenantID: "tenant-a", RuntimeAgentID: "runtime-a", SourceEpoch: "epoch-a",
		SourceSequence: 4, InventoryRevision: 4, ObservedAt: now,
		ListenersComplete: true, OpenPorts: []string{"tcp://0.0.0.0:443", "tcp://127.0.0.1:8080"},
	}
	if err := authority.RecordGuardPorts(t.Context(), first); err != nil {
		t.Fatalf("RecordGuardPorts(first): %v", err)
	}
	if err := authority.RecordGuardPorts(t.Context(), GuardObservation{
		TenantID: "tenant-a", RuntimeAgentID: "runtime-a", SourceEpoch: "epoch-a",
		SourceSequence: 3, InventoryRevision: 3, ObservedAt: now.Add(-time.Second),
		ListenersComplete: true, OpenPorts: []string{"tcp://0.0.0.0:22"},
	}); err != nil {
		t.Fatalf("RecordGuardPorts(replay): %v", err)
	}
	inventory, err := authority.ReadCurrent(t.Context(), InventoryRequest{
		TenantID: "tenant-a", ServerID: "server-a", OwnerSubjectID: "owner-a",
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("ReadCurrent: %v", err)
	}
	if len(inventory.Allocations) != 2 || inventory.Allocations[0].Port != 443 ||
		inventory.Allocations[0].DriftState != DriftConsistent || !inventory.Allocations[0].Desired ||
		inventory.Allocations[1].Port != 8080 || inventory.Allocations[1].DriftState != DriftUnexpected ||
		inventory.InventoryRevision != 4 {
		t.Fatalf("projected inventory = %#v", inventory)
	}
	if _, err := authority.ReadCurrent(t.Context(), InventoryRequest{
		TenantID: "tenant-a", ServerID: "server-a", OwnerSubjectID: "owner-b",
	}, now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign owner ReadCurrent error = %v, want not found", err)
	}
	stale, err := authority.ReadCurrent(t.Context(), InventoryRequest{
		TenantID: "tenant-a", ServerID: "server-a", OwnerSubjectID: "owner-a",
	}, now.Add(portObservationTTL+time.Second))
	if err != nil {
		t.Fatalf("ReadCurrent(stale): %v", err)
	}
	if stale.Allocations[0].ObservedState != EvidenceStale || stale.Allocations[0].DriftState != DriftUnknown ||
		stale.Allocations[1].ObservedState != EvidenceStale || stale.Allocations[1].DriftState != DriftUnknown {
		t.Fatalf("stale listener evidence asserted current drift: %#v", stale.Allocations)
	}
}

func assertExactTeardownSnapshotLifecycle(t *testing.T, database *sql.DB) {
	t.Helper()
	authority := NewPostgresAuthority(database)
	for _, request := range []AdmissionRequest{
		postgresIntegrationAdmission("tenant-a", "server-a", 3, "stack-a", "plan-a", "127.0.0.1"),
		postgresIntegrationAdmission("tenant-a", "server-c", 5, "stack-a", "plan-b", "127.0.0.2"),
	} {
		if _, err := authority.Admit(t.Context(), request); err != nil {
			t.Fatalf("Admit(%s): %v", request.ServerID, err)
		}
	}
	request := TeardownSnapshotRequest{TenantID: "tenant-a", OwnerSubjectID: "owner-a", TechstackID: "stack-a"}
	if _, err := authority.SnapshotForTeardown(t.Context(), TeardownSnapshotRequest{
		TenantID: "tenant-a", OwnerSubjectID: "owner-b", TechstackID: "stack-a",
	}); !errors.Is(err, ErrTeardownSnapshotMismatch) {
		t.Fatalf("foreign Owner snapshot error = %v, want ErrTeardownSnapshotMismatch", err)
	}
	snapshot, err := authority.SnapshotForTeardown(t.Context(), request)
	if err != nil || len(snapshot.Generations) != 2 {
		t.Fatalf("SnapshotForTeardown = %+v, err %v", snapshot, err)
	}
	partial, err := sealTeardownSnapshot(request, snapshot.Generations[:1])
	if err != nil {
		t.Fatalf("seal partial snapshot: %v", err)
	}
	if err := authority.ReleaseTeardownSnapshot(t.Context(), partial); !errors.Is(err, ErrTeardownSnapshotMismatch) {
		t.Fatalf("partial release error = %v, want ErrTeardownSnapshotMismatch", err)
	}
	tampered := snapshot
	tampered.Generations = append([]TeardownGeneration(nil), snapshot.Generations...)
	tampered.Generations[0].ClaimSetDigest = "sha256:" + strings.Repeat("f", 64)
	if err := authority.ReleaseTeardownSnapshot(t.Context(), tampered); !errors.Is(err, ErrTeardownSnapshotMismatch) {
		t.Fatalf("tampered release error = %v, want ErrTeardownSnapshotMismatch", err)
	}
	assertPortInventoryStateCount(t, database, "server_port_claim_generations", "released", 0)
	late := postgresIntegrationAdmission("tenant-a", "server-a", 3, "stack-a", "plan-c", "127.0.0.1")
	late.Requirements[0].Port = 8443
	if _, err := authority.Admit(t.Context(), late); err != nil {
		t.Fatalf("late Admit: %v", err)
	}
	if err := authority.ReleaseTeardownSnapshot(t.Context(), snapshot); !errors.Is(err, ErrTeardownSnapshotMismatch) {
		t.Fatalf("stale release error = %v, want ErrTeardownSnapshotMismatch", err)
	}
	assertPortInventoryStateCount(t, database, "server_port_claim_generations", "released", 0)
	snapshot, err = authority.SnapshotForTeardown(t.Context(), request)
	if err != nil || len(snapshot.Generations) != 3 {
		t.Fatalf("refreshed SnapshotForTeardown = %+v, err %v", snapshot, err)
	}
	if err := authority.ReleaseTeardownSnapshot(t.Context(), snapshot); err != nil {
		t.Fatalf("ReleaseTeardownSnapshot: %v", err)
	}
	if err := authority.ReleaseTeardownSnapshot(t.Context(), snapshot); err != nil {
		t.Fatalf("ReleaseTeardownSnapshot(replay): %v", err)
	}
	assertPortInventoryStateCount(t, database, "server_port_claim_generations", "released", 3)
	assertPortInventoryStateCount(t, database, "server_port_reservations", "released", 3)
}

func openPortInventoryIntegrationDatabase(t *testing.T) (*sql.DB, *sql.DB, *pgx.ConnConfig, string) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL"))
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping port inventory PostgreSQL integration test")
	}

	adminConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse port inventory integration DSN: %v", err)
	}
	adminDatabase := stdlib.OpenDB(*adminConfig)
	if err := adminDatabase.PingContext(t.Context()); err != nil {
		_ = adminDatabase.Close()
		t.Fatalf("ping port inventory integration PostgreSQL: %v", err)
	}

	schema := "port_inventory_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := adminDatabase.ExecContext(t.Context(), "CREATE SCHEMA "+quotedSchema); err != nil {
		_ = adminDatabase.Close()
		t.Fatalf("create port inventory integration schema: %v", err)
	}

	scopedConfig := adminConfig.Copy()
	scopedConfig.RuntimeParams = cloneRuntimeParams(adminConfig.RuntimeParams)
	scopedConfig.RuntimeParams["search_path"] = schema + ",public"
	database := stdlib.OpenDB(*scopedConfig)
	database.SetMaxOpenConns(8)
	database.SetMaxIdleConns(8)
	if err := database.PingContext(t.Context()); err != nil {
		_ = database.Close()
		_, _ = adminDatabase.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		_ = adminDatabase.Close()
		t.Fatalf("open scoped port inventory integration database: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
		cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = adminDatabase.ExecContext(cleanupContext, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
		_ = adminDatabase.Close()
	})

	applyPortInventoryIntegrationSchema(t, database)
	return database, adminDatabase, adminConfig, schema
}

func applyPortInventoryIntegrationSchema(t *testing.T, database *sql.DB) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), `
		CREATE TABLE techstack_tenants (
			id text PRIMARY KEY,
			display_name text NOT NULL,
			kind text NOT NULL,
			status text NOT NULL
		);
		CREATE TABLE stacks (
			id text PRIMARY KEY,
			tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE CASCADE,
			owner_subject_id text,
			status text NOT NULL DEFAULT 'running',
			deleted_at timestamptz
		);
		CREATE TABLE servers (
			id text PRIMARY KEY,
			tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE CASCADE,
			stack_id text REFERENCES stacks(id) ON DELETE SET NULL,
			owner_subject_id text NOT NULL,
			generation bigint NOT NULL,
			lifecycle_state text NOT NULL,
			worker_id text
		);
	`); err != nil {
		t.Fatalf("create port inventory prerequisite schema: %v", err)
	}
	for _, name := range []string{"064_server_port_inventory.sql", "065_align_server_port_exposure_contract.sql"} {
		migrationPath := filepath.Join("..", "..", "pkg", "db", "migrations", name)
		migration, err := os.ReadFile(migrationPath)
		if err != nil {
			t.Fatalf("read port inventory migration %s: %v", name, err)
		}
		if _, err := database.ExecContext(t.Context(), string(migration)); err != nil {
			t.Fatalf("apply port inventory migration %s: %v", name, err)
		}
	}
	if _, err := database.ExecContext(t.Context(), `
		INSERT INTO techstack_tenants (id, display_name, kind, status) VALUES
			('tenant-a', 'Tenant A', 'saas', 'active'),
			('tenant-b', 'Tenant B', 'saas', 'active');
		INSERT INTO stacks (id, tenant_id, owner_subject_id) VALUES
			('stack-a', 'tenant-a', 'owner-a'),
			('stack-b', 'tenant-b', 'owner-b');
		INSERT INTO servers (id, tenant_id, stack_id, owner_subject_id, generation, lifecycle_state, worker_id) VALUES
			('server-a', 'tenant-a', 'stack-a', 'owner-a', 3, 'active', 'runtime-a'),
			('server-b', 'tenant-b', 'stack-b', 'owner-b', 7, 'active', 'runtime-b'),
			('server-c', 'tenant-a', 'stack-a', 'owner-a', 5, 'active', 'runtime-c');
	`); err != nil {
		t.Fatalf("seed port inventory integration schema: %v", err)
	}
}

func assertPortInventoryStateCount(t *testing.T, database *sql.DB, table, state string, want int) {
	t.Helper()
	var got int
	if err := database.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table+" WHERE state = $1", state).Scan(&got); err != nil {
		t.Fatalf("count %s state %s: %v", table, state, err)
	}
	if got != want {
		t.Fatalf("%s state %s count = %d, want %d", table, state, got, want)
	}
}

func postgresIntegrationAdmission(tenantID, serverID string, generation int64, stackID, planHash, bindAddress string) AdmissionRequest {
	return AdmissionRequest{
		ServerRef:        ServerRef{TenantID: tenantID, ServerID: serverID, ServerGeneration: generation},
		StackID:          stackID,
		ResolvedPlanHash: planHash,
		Requirements: []Requirement{{
			ID:          "https",
			NodeRef:     "node-web",
			Transport:   TransportTCP,
			BindAddress: bindAddress,
			Port:        443,
			Sharing:     SharingExclusive,
			Exposure:    ExposurePublic,
		}},
	}
}

func assertPortInventoryRowCount(t *testing.T, database *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := database.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s row count = %d, want %d", table, got, want)
	}
}

func openRestrictedPortInventoryDatabase(
	t *testing.T,
	adminDatabase *sql.DB,
	adminConfig *pgx.ConnConfig,
	schema string,
) *sql.DB {
	t.Helper()
	var superuser, createRole bool
	if err := adminDatabase.QueryRowContext(t.Context(), `
		SELECT rolsuper, rolcreaterole FROM pg_roles WHERE rolname = current_user
	`).Scan(&superuser, &createRole); err != nil {
		t.Fatalf("inspect port inventory integration role authority: %v", err)
	}
	if !superuser && !createRole {
		t.Skip("integration PostgreSQL role cannot create a restricted NOBYPASSRLS role")
	}

	role := "port_inventory_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	password := "port_inventory_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedRole := pgx.Identifier{role}.Sanitize()
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := adminDatabase.ExecContext(t.Context(), fmt.Sprintf(
		"CREATE ROLE %s LOGIN PASSWORD '%s' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS",
		quotedRole, password,
	)); err != nil {
		t.Fatalf("create restricted port inventory role: %v", err)
	}

	var restricted *sql.DB
	t.Cleanup(func() {
		if restricted != nil {
			_ = restricted.Close()
		}
		cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = adminDatabase.ExecContext(cleanupContext, "DROP OWNED BY "+quotedRole)
		_, _ = adminDatabase.ExecContext(cleanupContext, "DROP ROLE IF EXISTS "+quotedRole)
	})
	if _, err := adminDatabase.ExecContext(t.Context(), "GRANT USAGE ON SCHEMA "+quotedSchema+" TO "+quotedRole); err != nil {
		t.Fatalf("grant port inventory schema usage: %v", err)
	}
	if _, err := adminDatabase.ExecContext(t.Context(), "GRANT SELECT ON "+quotedSchema+".server_port_reservations TO "+quotedRole); err != nil {
		t.Fatalf("grant port inventory reservation read: %v", err)
	}

	restrictedConfig := adminConfig.Copy()
	restrictedConfig.User = role
	restrictedConfig.Password = password
	restrictedConfig.RuntimeParams = cloneRuntimeParams(adminConfig.RuntimeParams)
	restrictedConfig.RuntimeParams["search_path"] = schema + ",public"
	restricted = stdlib.OpenDB(*restrictedConfig)
	if err := restricted.PingContext(t.Context()); err != nil {
		t.Fatalf("connect as restricted port inventory role: %v", err)
	}
	return restricted
}

func assertRestrictedTenantCount(t *testing.T, database *sql.DB, tenantID string, want int) {
	t.Helper()
	tx, err := database.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("begin restricted port inventory transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), "SELECT set_config('app.tenant_id', $1, true)", tenantID); err != nil {
		t.Fatalf("set restricted port inventory tenant: %v", err)
	}
	var got int
	if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM server_port_reservations").Scan(&got); err != nil {
		t.Fatalf("query restricted port inventory: %v", err)
	}
	if got != want {
		t.Fatalf("restricted tenant %s row count = %d, want %d", tenantID, got, want)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit restricted port inventory transaction: %v", err)
	}
}

func cloneRuntimeParams(values map[string]string) map[string]string {
	result := make(map[string]string, len(values)+1)
	for key, value := range values {
		result[key] = value
	}
	return result
}
