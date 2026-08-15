package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIntegrationWorkerIdentityIsTenantBound(t *testing.T) {
	database := openTestDB(t)
	ctx := context.Background()
	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	const workerID = "integration-shared-worker"
	for _, tenantID := range []string{"integration-worker-tenant-a", "integration-worker-tenant-b"} {
		err := database.WithTenant(ctx, tenantID, func(tx *sql.Tx) error {
			_, execErr := tx.ExecContext(ctx, `
				INSERT INTO workers (tenant_id, id, hostname)
				VALUES ($1, $2, $3)
			`, tenantID, workerID, tenantID+"-host")
			return execErr
		})
		if err != nil {
			t.Fatalf("insert duplicate worker id for %s: %v", tenantID, err)
		}

		var count int
		err = database.WithTenant(ctx, tenantID, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `
				SELECT count(*) FROM workers WHERE tenant_id = $1 AND id = $2
			`, tenantID, workerID).Scan(&count)
		})
		if err != nil {
			t.Fatalf("read worker for %s: %v", tenantID, err)
		}
		if count != 1 {
			t.Fatalf("worker count for %s = %d, want 1", tenantID, count)
		}
	}

	const tenantAOnlyWorker = "integration-tenant-a-only-worker"
	if err := database.WithTenant(ctx, "integration-worker-tenant-a", func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx, `
			INSERT INTO workers (tenant_id, id, hostname)
			VALUES ($1, $2, 'tenant-a-only-host')
		`, "integration-worker-tenant-a", tenantAOnlyWorker)
		return execErr
	}); err != nil {
		t.Fatalf("insert tenant-a-only worker: %v", err)
	}

	err := database.WithTenant(ctx, "integration-worker-tenant-b", func(tx *sql.Tx) error {
		_, execErr := tx.ExecContext(ctx, `
			INSERT INTO agent_commands (id, tenant_id, worker_id, type)
			VALUES ('integration-cross-tenant-command', $1, $2, 'inventory')
		`, "integration-worker-tenant-b", tenantAOnlyWorker)
		return execErr
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("cross-tenant worker reference error = %v, want SQLSTATE 23503", err)
	}

	expected := map[string]string{
		"workers_pkey":                      "PRIMARY KEY (tenant_id, id)",
		"agent_commands_worker_tenant_fk":   "FOREIGN KEY (tenant_id, worker_id) REFERENCES workers(tenant_id, id) ON DELETE CASCADE",
		"precheck_results_worker_tenant_fk": "FOREIGN KEY (tenant_id, worker_id) REFERENCES workers(tenant_id, id) ON DELETE SET NULL (worker_id)",
		"nodes_worker_tenant_fk":            "FOREIGN KEY (tenant_id, worker_id) REFERENCES workers(tenant_id, id) ON DELETE SET NULL (worker_id)",
		"servers_worker_tenant_fk":          "FOREIGN KEY (tenant_id, worker_id) REFERENCES workers(tenant_id, id) ON DELETE RESTRICT",
	}
	rows, err := database.QueryContext(ctx, `
		SELECT constraint_name, pg_get_constraintdef(oid)
		FROM (
			SELECT conname AS constraint_name, oid
			FROM pg_constraint
			WHERE conname IN (
				'workers_pkey',
				'agent_commands_worker_tenant_fk',
				'precheck_results_worker_tenant_fk',
				'nodes_worker_tenant_fk',
				'servers_worker_tenant_fk'
			)
			  AND connamespace = current_schema()::regnamespace
		) AS constraints
	`)
	if err != nil {
		t.Fatalf("query worker constraints: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			t.Fatalf("scan worker constraint: %v", err)
		}
		want, ok := expected[name]
		if !ok {
			t.Fatalf("unexpected worker constraint %q", name)
		}
		if !strings.Contains(definition, want) {
			t.Fatalf("%s = %q, want it to contain %q", name, definition, want)
		}
		delete(expected, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate worker constraints: %v", err)
	}
	if len(expected) != 0 {
		t.Fatalf("missing worker constraints after migration: %v", expected)
	}
}
