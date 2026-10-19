package db_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	pgkdb "github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/guardposture"
)

// Repairs a Guard ran under its local policy count toward the persisted circuit
// breaker: after a control-plane restart and a Guard reconnect, the breaker the
// earlier failures opened still refuses the next local repair, and a repeat of
// already committed evidence is still acknowledged as a duplicate.
func TestIntegrationLocalRepairBudgetSurvivesReconnect(t *testing.T) {
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
	start := time.Now().UTC().Truncate(time.Second)
	now := start
	boot := func() *guardposture.Service {
		store, err := guardposture.NewPostgresStore(database.DB, nil)
		if err != nil {
			t.Fatal(err)
		}
		service, err := guardposture.NewService(guardposture.ServiceConfig{Store: store, Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		return service
	}
	suffix := uuid.NewString()
	identity := guardposture.Identity{TenantID: "org_local" + suffix, WorkerID: "worker-" + suffix, ServerID: "server-" + suffix}

	first := boot()
	policy, err := first.SetAutoRepair(t.Context(), identity.TenantID, identity.WorkerID, "auth0|owner", true,
		[]string{guardposture.ControlFirewall, guardposture.ControlBruteForce}, start.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	evidence := func(operationID, control string, started time.Time) guardposture.LocalOperationEvidence {
		e := guardposture.LocalOperationEvidence{
			OperationID: operationID, TenantID: identity.TenantID, WorkerID: identity.WorkerID, Generation: 1,
			PolicyID: policy.PolicyID, PolicyVersion: policy.Version, PolicyExpiresAt: *policy.ExpiresAt,
			Action: guardposture.LocalActionHostSecurityRepair, Controls: []string{control}, StartedAt: started,
			Report:  guardbinding.ResultReport{Revision: 1, State: guardbinding.ReportTerminal},
			Outcome: "failed", Steps: []guardposture.RepairStep{{Control: control, Status: "failed", ReasonCode: "apply_failed"}},
		}
		if e.Report.Digest, err = e.ComputeDigest(); err != nil {
			t.Fatal(err)
		}
		return e
	}
	failures := []guardposture.LocalOperationEvidence{
		evidence("op-1", guardposture.ControlFirewall, start),
		evidence("op-2", guardposture.ControlBruteForce, start.Add(5*time.Minute)),
		evidence("op-3", guardposture.ControlFirewall, start.Add(40*time.Minute)),
	}
	for _, e := range failures {
		now = e.StartedAt.Add(time.Minute)
		if _, err := first.AcceptLocalOperation(t.Context(), identity, "node", 1, e); err != nil {
			t.Fatalf("%s: %v", e.OperationID, err)
		}
	}
	first.Close()

	// The control plane restarts and the Guard reconnects with a rotated
	// credential; nothing about the budget lives in either connection.
	second := boot()
	t.Cleanup(second.Close)
	now = start.Add(51 * time.Minute)
	_, err = second.AcceptLocalOperation(t.Context(), identity, "node", 2, evidence("op-4", guardposture.ControlBruteForce, start.Add(50*time.Minute)))
	var denial *guardbinding.Denial
	if !errors.As(err, &denial) || denial.Code != guardposture.DenyCircuitOpen {
		t.Fatalf("a local repair after three failures was not refused by the breaker: %v", err)
	}
	ack, err := second.AcceptLocalOperation(t.Context(), identity, "node", 2, failures[0])
	if err != nil || !ack.Duplicate {
		t.Fatalf("committed evidence after reconnect: %+v %v", ack, err)
	}
	repairs, err := second.Repairs(t.Context(), identity.TenantID, identity.WorkerID, 0)
	if err != nil || len(repairs) != len(failures) {
		t.Fatalf("recorded local repairs = %d, %v", len(repairs), err)
	}
}
