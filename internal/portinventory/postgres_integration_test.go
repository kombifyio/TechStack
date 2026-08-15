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
		CREATE TABLE servers (
			id text PRIMARY KEY,
			tenant_id text NOT NULL REFERENCES techstack_tenants(id) ON DELETE CASCADE,
			generation bigint NOT NULL,
			lifecycle_state text NOT NULL
		);
	`); err != nil {
		t.Fatalf("create port inventory prerequisite schema: %v", err)
	}
	migrationPath := filepath.Join("..", "..", "pkg", "db", "migrations", "064_server_port_inventory.sql")
	migration, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read port inventory migration: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), string(migration)); err != nil {
		t.Fatalf("apply port inventory migration: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), `
		INSERT INTO techstack_tenants (id, display_name, kind, status) VALUES
			('tenant-a', 'Tenant A', 'saas', 'active'),
			('tenant-b', 'Tenant B', 'saas', 'active');
		INSERT INTO servers (id, tenant_id, generation, lifecycle_state) VALUES
			('server-a', 'tenant-a', 3, 'active'),
			('server-b', 'tenant-b', 7, 'active');
	`); err != nil {
		t.Fatalf("seed port inventory integration schema: %v", err)
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
