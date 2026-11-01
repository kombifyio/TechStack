package db

import (
	"context"
	"database/sql"
	"errors"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// Sensitive migration boundary (platform-cesjv.2): migration 176 makes a
// managed target's SSH host-key pin follow its managed lease. The database is
// owned by a non-bypass migration owner, as production's is, so FORCE RLS
// holds for every read the backfill and the triggers make.
func TestIntegrationRuntimeHostKeyPinLifecycle(t *testing.T) {
	dsn := integrationDSN()
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping Postgres integration test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	target, _, _ := newForwardBatchDatabase(t, ctx, dsn)
	pinApplyMigrationsBefore(t, ctx, target, "176_")

	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	tenant, otherTenant := "pin-tenant-"+suffix, "pin-other-"+suffix
	for _, id := range []string{tenant, otherTenant} {
		pinExecInTenant(t, ctx, target.DB, id, `INSERT INTO techstack_tenants (id, display_name) VALUES ($1, $1)`, id)
	}
	lease := func(tenantID, id, host string) {
		generation := uuid.NewString()
		pinExecInTenant(t, ctx, target.DB, tenantID, `INSERT INTO techstack_vm_leases
			(id, tenant_id, subject_id, provider_id, desired_state, resource_generation_id, lease_json)
			VALUES ($1, $2, 'owner', 'centron', 'running', $3::uuid,
			        jsonb_build_object('metadata', jsonb_build_object('resource_generation_id', $3::text, 'runtime_public_ip', $4::text)))`,
			id, tenantID, generation, host)
	}
	generationOf := func(tx *sql.Tx, tenantID, leaseID string) string {
		var generation string
		if err := tx.QueryRowContext(ctx, `SELECT resource_generation_id::text FROM techstack_vm_leases WHERE tenant_id = $1 AND id = $2`,
			tenantID, leaseID).Scan(&generation); err != nil {
			t.Fatal(err)
		}
		return generation
	}
	// absent records a terminal provider operation of the lease and, when
	// proven, its provider_api absence observation.
	absent := func(tenantID, leaseID, kind string, proven bool, generationOverride string) {
		operation := "op-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
		tx := pinBeginInTenant(t, ctx, target.DB, tenantID)
		defer func() { _ = tx.Rollback() }()
		generation := generationOverride
		if generation == "" {
			generation = generationOf(tx, tenantID, leaseID)
		}
		if proven {
			pinInsertAbsenceObservation(t, ctx, tx, tenantID, operation)
		}
		pinInsertProviderOperation(t, ctx, tx, tenantID, operation, leaseID, kind, generation, "succeeded", "absent")
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	diskPin := func(address, fingerprint string) {
		pinExec(t, ctx, target.DB, `INSERT INTO runtime_host_key_pins (address, fingerprint, source) VALUES ($1, $2, 'disk_import')`,
			address, fingerprint)
	}
	// observedPin is the runtime's bound first contact (diskcustody.PinFirst).
	observedPin := func(tenantID, leaseID, address, fingerprint string) error {
		tx := pinBeginInTenant(t, ctx, target.DB, tenantID)
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx, `SELECT set_config('app.runtime_host_key_lease', $1, true)`, leaseID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO runtime_host_key_pins (address, fingerprint, source) VALUES ($1, $2, 'observed')
			ON CONFLICT (address) DO NOTHING`, address, fingerprint); err != nil {
			return err
		}
		return tx.Commit()
	}
	binding := func(tenantID, leaseID string) string {
		var digest string
		if err := target.QueryRowContext(ctx, `SELECT runtime_host_key_pin_lease_binding($1, $2)`, tenantID, leaseID).Scan(&digest); err != nil {
			t.Fatal(err)
		}
		return digest
	}

	// The provider ledger guards demand an execution claim and receipt chain
	// that this case does not exercise; the migration's own triggers stay on.
	for _, statement := range []string{
		"ALTER TABLE provider_operations DISABLE TRIGGER USER",
		"ALTER TABLE techstack_vm_leases DISABLE TRIGGER USER",
	} {
		pinExec(t, ctx, target.DB, statement)
	}

	// Before 176: unbound pins. A lease counts for a pin only if it existed
	// when the pin was taken.
	lease(tenant, "lease-gone-"+suffix, "203.0.113.10")
	absent(tenant, "lease-gone-"+suffix, "decommission", true, "")
	diskPin("203.0.113.10:22", "SHA256:gone")

	lease(tenant, "lease-gone2-"+suffix, "203.0.113.11")
	absent(tenant, "lease-gone2-"+suffix, "decommission", true, "")
	lease(otherTenant, "lease-live-"+suffix, "203.0.113.11")
	diskPin("203.0.113.11:22", "SHA256:live")

	diskPin("203.0.113.12:22", "SHA256:unknown")

	lease(tenant, "lease-live2-"+suffix, "203.0.113.13")
	lease(otherTenant, "lease-live3-"+suffix, "203.0.113.13")
	diskPin("203.0.113.13:22", "SHA256:ambiguous")

	lease(tenant, "lease-unproven-"+suffix, "203.0.113.14")
	absent(tenant, "lease-unproven-"+suffix, "observe", true, "")
	diskPin("203.0.113.14:22", "SHA256:unproven")

	// The recycled address of the incident: the old server is proven gone,
	// the new server's lease came after the pin.
	lease(tenant, "lease-old-"+suffix, "203.0.113.15")
	absent(tenant, "lease-old-"+suffix, "decommission", true, "")
	diskPin("203.0.113.15:22", "SHA256:recycled")
	time.Sleep(10 * time.Millisecond)
	lease(otherTenant, "lease-recycled-"+suffix, "203.0.113.15")

	pinApplyMigration(t, ctx, target, "176_runtime_host_key_pin_lifecycle.sql")
	pinExec(t, ctx, target.DB, "ALTER TABLE provider_operations DISABLE TRIGGER USER")
	pinExec(t, ctx, target.DB, "ALTER TABLE provider_operations ENABLE TRIGGER runtime_host_key_pins_retire_absent_lease")

	type pinRow struct{ fingerprint, binding string }
	readPin := func(address string) (pinRow, bool) {
		var row pinRow
		var bound sql.NullString
		err := target.QueryRowContext(ctx, `SELECT fingerprint, lease_binding FROM runtime_host_key_pins WHERE address = $1`, address).
			Scan(&row.fingerprint, &bound)
		if errors.Is(err, sql.ErrNoRows) {
			return row, false
		}
		if err != nil {
			t.Fatal(err)
		}
		row.binding = bound.String
		return row, true
	}
	retiredReason := func(address, fingerprint string) string {
		var reason string
		err := target.QueryRowContext(ctx, `SELECT reason FROM runtime_host_key_pin_retirements WHERE address = $1 AND fingerprint = $2`,
			address, fingerprint).Scan(&reason)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
		return reason
	}

	// Backfill.
	for _, address := range []string{"203.0.113.10:22", "203.0.113.15:22"} {
		if _, ok := readPin(address); ok || retiredReason(address, map[string]string{
			"203.0.113.10:22": "SHA256:gone", "203.0.113.15:22": "SHA256:recycled",
		}[address]) != "backfill_provider_absence_verified" {
			t.Fatalf("%s: a pin whose every earlier lease is proven absent must retire with its evidence", address)
		}
	}
	if row, ok := readPin("203.0.113.11:22"); !ok || row.binding != binding(otherTenant, "lease-live-"+suffix) {
		t.Fatalf("a pin whose host one earlier live lease records must bind to it: %+v %v", row, ok)
	}
	for _, address := range []string{"203.0.113.12:22", "203.0.113.13:22"} {
		if row, ok := readPin(address); !ok || row.binding != "" {
			t.Fatalf("%s: an unprovable pin must stay unbound: %+v %v", address, row, ok)
		}
	}
	if row, ok := readPin("203.0.113.14:22"); !ok || row.binding != binding(tenant, "lease-unproven-"+suffix) {
		t.Fatalf("an observe absence is no decommission proof; the pin stays with its lease: %+v %v", row, ok)
	}

	// Admit: a binding holds only for the writer's own lease recording the host.
	lease(tenant, "lease-new-"+suffix, "203.0.113.20")
	if err := observedPin(tenant, "lease-new-"+suffix, "203.0.113.20:22", "SHA256:new"); err != nil {
		t.Fatal(err)
	}
	if row, _ := readPin("203.0.113.20:22"); row.binding != binding(tenant, "lease-new-"+suffix) {
		t.Fatalf("a pin taken for the lease recording the host must stay bound: %+v", row)
	}
	if err := observedPin(tenant, "lease-new-"+suffix, "203.0.113.21:22", "SHA256:elsewhere"); err != nil {
		t.Fatal(err)
	}
	if err := observedPin(tenant, "lease-live-"+suffix, "203.0.113.11:2222", "SHA256:foreign"); err != nil {
		t.Fatal(err)
	}
	pinExecInTenant(t, ctx, target.DB, tenant, `INSERT INTO runtime_host_key_pins (address, fingerprint, source, lease_binding)
		VALUES ('203.0.113.22:22', 'SHA256:forged', 'observed', $1)`, binding(tenant, "lease-gone-"+suffix))
	for _, address := range []string{"203.0.113.21:22", "203.0.113.11:2222", "203.0.113.22:22"} {
		if row, ok := readPin(address); !ok || row.binding != "" {
			t.Fatalf("%s: a binding the writer's tenant scope cannot prove must be dropped: %+v %v", address, row, ok)
		}
	}
	var refusal *pgconn.PgError
	if err := observedPin(tenant, "lease-gone-"+suffix, "203.0.113.10:22", "SHA256:late"); !errors.As(err, &refusal) || refusal.Code != "55000" {
		t.Fatalf("a pin for a lease proven absent must be refused, got %v", err)
	}
	// A retired pin never comes back from a disk.
	diskPin("203.0.113.10:22", "SHA256:gone")
	if _, ok := readPin("203.0.113.10:22"); ok {
		t.Fatal("a retired pin must not be re-imported from a disk")
	}

	// Retire: only a proven decommission of the lease's current generation
	// retires the pins bound to that lease.
	absent(tenant, "lease-new-"+suffix, "decommission", false, "")
	absent(tenant, "lease-new-"+suffix, "decommission", true, uuid.NewString())
	absent(tenant, "lease-new-"+suffix, "observe", true, "")
	if _, ok := readPin("203.0.113.20:22"); !ok {
		t.Fatal("an unproven, other-generation or observe absence must not retire a pin")
	}
	operation := "op-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	tx := pinBeginInTenant(t, ctx, target.DB, tenant)
	pinInsertProviderOperation(t, ctx, tx, tenant, operation, "lease-new-"+suffix, "decommission",
		generationOf(tx, tenant, "lease-new-"+suffix), "pending", "delete_accepted")
	pinInsertAbsenceObservation(t, ctx, tx, tenant, operation)
	if _, err := tx.ExecContext(ctx, `UPDATE provider_operations SET status = 'succeeded', phase = 'absent' WHERE tenant_id = $1 AND operation_id = $2`,
		tenant, operation); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, ok := readPin("203.0.113.20:22"); ok || retiredReason("203.0.113.20:22", "SHA256:new") != "provider_absence_verified" {
		t.Fatal("the proven decommission of the lease must retire its pin")
	}
	if _, ok := readPin("203.0.113.11:22"); !ok {
		t.Fatal("another lease's pin must survive")
	}
	lease(otherTenant, "lease-next-"+suffix, "203.0.113.20")
	if err := observedPin(otherTenant, "lease-next-"+suffix, "203.0.113.20:22", "SHA256:next"); err != nil {
		t.Fatal(err)
	}
	if row, ok := readPin("203.0.113.20:22"); !ok || row.fingerprint != "SHA256:next" || row.binding != binding(otherTenant, "lease-next-"+suffix) {
		t.Fatalf("the next server at the address starts a fresh pin: %+v %v", row, ok)
	}
}

func pinApplyMigrationsBefore(t *testing.T, ctx context.Context, target *DB, stopPrefix string) {
	t.Helper()
	pinExec(t, ctx, target.DB, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() >= stopPrefix {
			continue
		}
		pinApplyMigration(t, ctx, target, entry.Name())
	}
}

func pinApplyMigration(t *testing.T, ctx context.Context, target *DB, name string) {
	t.Helper()
	body, err := migrationsFS.ReadFile(path.Join("migrations", name))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := executePendingMigration(ctx, tx, name, body); err != nil {
		_ = tx.Rollback()
		t.Fatalf("migration %s: %v", name, err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version) VALUES($1)", name); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func pinExec(t *testing.T, ctx context.Context, database *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := database.ExecContext(ctx, statement, args...); err != nil {
		t.Fatalf("%s: %v", strings.Join(strings.Fields(statement), " "), err)
	}
}

func pinBeginInTenant(t *testing.T, ctx context.Context, database *sql.DB, tenantID string) *sql.Tx {
	t.Helper()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	return tx
}

func pinExecInTenant(t *testing.T, ctx context.Context, database *sql.DB, tenantID, statement string, args ...any) {
	t.Helper()
	tx := pinBeginInTenant(t, ctx, database, tenantID)
	if _, err := tx.ExecContext(ctx, statement, args...); err != nil {
		_ = tx.Rollback()
		t.Fatalf("%s: %v", strings.Join(strings.Fields(statement), " "), err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func pinInsertProviderOperation(t *testing.T, ctx context.Context, tx *sql.Tx, tenantID, operationID, leaseID, kind, generation, status, phase string) {
	t.Helper()
	digest := "sha256:" + strings.Repeat("a", 64)
	if _, err := tx.ExecContext(ctx, `INSERT INTO provider_operations
		(tenant_id, operation_id, lease_id, operation, idempotency_key, adapter_id, command_digest, command_json,
		 ledger_revision, status, phase, head_sequence, head_receipt_digest, requested_at, created_at, updated_at, provision_dispatch_mode)
		VALUES ($1, $2, $3, $8, $2, 'centron', $4,
		        jsonb_build_object('command', jsonb_build_object('resource_generation_id', $5::text)),
		        1, $6, $7, 1, $4, now(), now(), now(), 'blocked')`,
		tenantID, operationID, leaseID, digest, generation, status, phase, kind); err != nil {
		t.Fatalf("insert provider operation: %v", err)
	}
}

func pinInsertAbsenceObservation(t *testing.T, ctx context.Context, tx *sql.Tx, tenantID, operationID string) {
	t.Helper()
	digest := "sha256:" + strings.Repeat("b", 64)
	if _, err := tx.ExecContext(ctx, `INSERT INTO provider_absence_observations
		(tenant_id, operation_id, binding_id, provider_id, evidence_ref, evidence_digest, attestation_ref, attestation_digest,
		 observation, source, subject_hash, native_ref_hash, collected_at, document_json)
		VALUES ($1, $2, 'server', 'centron', 'provider-evidence://' || $2, $3, 'provider-attestation://' || $2, $3,
		        'absent', 'provider_api', $3, $3, now(), '{}'::jsonb)`,
		tenantID, operationID, digest); err != nil {
		t.Fatalf("insert absence observation: %v", err)
	}
}
