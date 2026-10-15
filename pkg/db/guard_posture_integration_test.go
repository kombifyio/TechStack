package db_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	pgkdb "github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/guardposture"
)

type postureAlerts struct {
	mu     sync.Mutex
	events []guardposture.AlertEvent
	tx     int
}

func (a *postureAlerts) EnqueueAlert(_ context.Context, tx *sql.Tx, event guardposture.AlertEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if tx != nil {
		a.tx++
	}
	a.events = append(a.events, event)
	return nil
}

// Posture is tenant scoped by row level security, one finding episode per key
// is open at a time and its alert is written inside the transaction that
// changed the ledger, and two concurrent repair requests cannot both pass the
// same admission check.
func TestIntegrationGuardPostureIsTenantScopedAndRepairAdmissionIsAtomic(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL"))
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping Postgres integration test")
	}
	database, err := pgkdb.Open(pgkdb.Config{Backend: pgkdb.StoreBackendPostgres, DSN: dsn, DriverName: pgkdb.PostgresDriverName})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	alerts := &postureAlerts{}
	store, err := guardposture.NewPostgresStore(database.DB, alerts)
	if err != nil {
		t.Fatal(err)
	}
	policy := guardposture.DefaultPolicy()
	now := time.Now().UTC().Truncate(time.Microsecond)
	service, err := guardposture.NewService(guardposture.ServiceConfig{Store: store, Policy: policy, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)

	suffix := uuid.NewString()
	tenantA, tenantB := "org_a"+suffix, "org_b"+suffix
	worker := "worker-" + suffix
	identA := guardposture.Identity{TenantID: tenantA, WorkerID: worker, ServerID: "server-a", OwnerSubjectID: "auth0|a"}
	controls := func(firewall string) []map[string]any {
		out := []map[string]any{}
		for _, id := range []string{"firewall.default_inbound", "ssh.password_authentication", "ssh.root_login", "ssh.port", "bruteforce.fail2ban",
			"updates.unattended_upgrades", "kernel.sysctl", "updates.pending_security", "updates.reboot_required"} {
			state := "compliant"
			if id == "firewall.default_inbound" {
				state = firewall
			}
			out = append(out, map[string]any{"id": id, "state": state, "expected": "e", "observed": "o", "observed_at": now, "remediation": map[string]any{"capability": "automatic"}})
		}
		return out
	}
	report := func(seq int, firewall string) []byte {
		evidence, _ := json.Marshal(map[string]any{
			"schema_version": guardposture.EvidenceSchema, "mode": "advanced", "site_kind": "home",
			"baseline_version": "stackkit.host-security-baseline/1.0.0", "observed_at": now, "expires_at": now.Add(45 * time.Minute),
			"freshness_budget_seconds": 2700, "overall": "compliant", "controls": controls(firewall),
		})
		body, _ := json.Marshal(guardposture.Report{
			SchemaVersion: guardposture.ReportSchema, SourceEpoch: "e1", Sequence: int64(seq), ObservedAt: now, GuardVersion: "v1",
			Security: &guardposture.SecurityReport{Status: guardposture.ObservationObserved, Evidence: evidence},
		})
		return body
	}
	if _, err := service.Ingest(t.Context(), identA, "node", report(1, "drifted")); err != nil {
		t.Fatal(err)
	}
	// A replayed report and a repeated drift open nothing new.
	if _, err := service.Ingest(t.Context(), identA, "node", report(1, "drifted")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Ingest(t.Context(), identA, "node", report(2, "drifted")); err != nil {
		t.Fatal(err)
	}
	if open, err := store.OpenFindings(t.Context(), tenantA, worker); err != nil || len(open) != 1 {
		t.Fatalf("open findings = %v, %v", open, err)
	}
	if alerts.tx != 1 || len(alerts.events) != 1 {
		t.Fatalf("alerts = %d (in a transaction: %d); one drift is one alert", len(alerts.events), alerts.tx)
	}
	if _, err := service.Ingest(t.Context(), identA, "node", report(3, "compliant")); err != nil {
		t.Fatal(err)
	}
	if open, _ := store.OpenFindings(t.Context(), tenantA, worker); len(open) != 0 || len(alerts.events) != 2 || !alerts.events[1].Resolved {
		t.Fatalf("after compliance: open %d, alerts %d", len(open), len(alerts.events))
	}

	// Another tenant reads nothing, and row level security is what says so.
	if _, err := store.Get(t.Context(), tenantB, worker); !errors.Is(err, guardposture.ErrNotFound) {
		t.Fatalf("another tenant read the posture: %v", err)
	}
	if listed, err := store.ListByTenant(t.Context(), tenantB); err != nil || len(listed) != 0 {
		t.Fatalf("another tenant listed %v, %v", listed, err)
	}
	if history, _ := store.Findings(t.Context(), tenantB, worker, time.Time{}, 10); len(history) != 0 {
		t.Fatalf("another tenant read findings history: %v", history)
	}
	// The suite connection may be a superuser, which bypasses row security, so
	// the posture itself is asserted from the catalog for every posture table.
	for _, table := range []string{"guard_posture_states", "guard_posture_findings", "guard_posture_repairs", "guard_repair_policies", "guard_telemetry_cursors"} {
		var enabled, forced bool
		var policies int
		if err := database.DB.QueryRowContext(t.Context(), `SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = $1::regclass`, table).Scan(&enabled, &forced); err != nil {
			t.Fatal(err)
		}
		if err := database.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_policies WHERE tablename = $1 AND policyname = 'tenant_isolation'
			AND qual LIKE '%app.tenant_id%' AND with_check LIKE '%app.tenant_id%'`, table).Scan(&policies); err != nil {
			t.Fatal(err)
		}
		if !enabled || !forced || policies != 1 {
			t.Fatalf("%s RLS posture: enabled=%v forced=%v policies=%d", table, enabled, forced, policies)
		}
	}

	// Two concurrent admissions of the same control: exactly one is admitted.
	if _, err := service.Ingest(t.Context(), identA, "node", report(4, "drifted")); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	admitted := make(chan bool, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			repair, err := store.AdmitRepair(t.Context(), tenantA, worker, now, func(history []guardposture.Repair, posture *guardposture.Posture) (*guardposture.Repair, error) {
				if err := policy.AdmitRepair(posture, history, []string{"firewall.default_inbound"}, now); err != nil {
					return nil, err
				}
				id := uuid.NewString()
				return &guardposture.Repair{ID: id, ServerID: "server-a", Mode: guardposture.RepairOperator, RequestedBy: "auth0|a",
					Controls: []string{"firewall.default_inbound"}, Status: guardposture.RepairQueued, CommandID: guardposture.RepairCommandID(id),
					RequestedAt: now, ExpiresAt: now.Add(10 * time.Minute)}, nil
			})
			admitted <- err == nil && repair != nil
		}()
	}
	wg.Wait()
	close(admitted)
	count := 0
	for ok := range admitted {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d concurrent repairs were admitted; exactly one may be", count)
	}
}
