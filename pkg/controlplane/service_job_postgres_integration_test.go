//go:build integration

package controlplane

import (
	"database/sql"
	"testing"

	"github.com/kombifyio/techstack/internal/localdb"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Invoke explicitly with -tags integration. This starts an owned local database
// using the existing embedded runtime, and fails rather than skipping evidence.
func TestPostgresServiceJobReservationKeepsCompleteTargetFence(t *testing.T) {
	embedded, err := localdb.StartEmbeddedPostgres(t.TempDir())
	if err != nil {
		t.Fatalf("start isolated PostgreSQL: %v", err)
	}
	t.Cleanup(func() {
		if err := embedded.Stop(); err != nil {
			t.Errorf("stop isolated PostgreSQL: %v", err)
		}
	})
	db, err := sql.Open("pgx", embedded.BootstrapDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// The fixture owns the exact jobs columns consumed by the production store;
	// stack/resource authorization is exercised by the real public route fixture.
	_, err = db.ExecContext(t.Context(), `CREATE TABLE jobs (
  id text PRIMARY KEY, tenant_id text NOT NULL, instance_id text, stack_id text,
  type text NOT NULL, state text NOT NULL, priority integer NOT NULL DEFAULT 0,
  progress integer NOT NULL DEFAULT 0, step text, message text, error text, error_details text,
  logs_json jsonb NOT NULL DEFAULT '[]', result_json jsonb NOT NULL DEFAULT '{}', payload_json jsonb NOT NULL DEFAULT '{}',
  scheduled_for timestamptz NOT NULL DEFAULT now(), started_at timestamptz, completed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
 )`)
	if err != nil {
		t.Fatal(err)
	}
	testServiceJobReservation(t, NewPostgresStore(db))
}
