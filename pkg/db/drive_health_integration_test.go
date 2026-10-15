package db_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	pgkdb "github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/drivehealth"
	"github.com/kombifyio/techstack/pkg/guardtelemetry"
	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type driveDelivered struct {
	events []monitoring.Alert
	fail   bool
}

func (n *driveDelivered) Name() string { return "integration-probe" }
func (n *driveDelivered) Notify(_ context.Context, a monitoring.Alert) error {
	if n.fail {
		return errors.New("transient notification failure")
	}
	n.events = append(n.events, a)
	return nil
}
func TestIntegrationDriveHealthPersistsEpisodesAndTenantIsolation(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL"))
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set")
	}
	database, err := pgkdb.Open(pgkdb.Config{Backend: pgkdb.StoreBackendPostgres, DSN: dsn, DriverName: pgkdb.PostgresDriverName})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err = database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	tenant := "drive-" + uuid.NewString()
	id := guardtelemetry.Identity{TenantID: tenant, ServerID: "server", WorkerID: "guard"}
	store, err := monitoring.NewPostgresDriveHealthStore(database.DB)
	if err != nil {
		t.Fatal(err)
	}
	service := &monitoring.DriveHealthService{Store: store, Now: func() time.Time { return now }}
	passed := false
	selfTestFailed := false
	temp := 35.0
	zero := "0"
	reading := drivehealth.Reading{DriveID: drivehealth.StableID("fixture", "serial", ""), DevicePath: "/dev/sda", Model: "fixture", Serial: "serial", Protocol: "ATA", Source: drivehealth.SourceSMART, ObservedAt: now, Status: drivehealth.Available, Missing: []string{}, TemperatureCelsius: &temp, SelfTestFailed: &selfTestFailed, SmartPassed: &passed, ReallocatedSectors: &zero, PendingSectors: &zero, UncorrectableSectors: &zero}
	collection := drivehealth.Collection{ObservedAt: now, Status: drivehealth.Available, Complete: true, Drives: []drivehealth.Reading{reading}}
	if err = service.Ingest(t.Context(), id, "epoch", 1, collection); err != nil {
		t.Fatal(err)
	}
	initial, err := service.Read(t.Context(), tenant, "server")
	if err != nil {
		t.Fatal(err)
	}
	episode := initial.Servers[0].Drives[0].Alerts[0].ID
	// Reconstruct the public store/service to prove continuity is durable.
	reopened, err := monitoring.NewPostgresDriveHealthStore(database.DB)
	if err != nil {
		t.Fatal(err)
	}
	service = &monitoring.DriveHealthService{Store: reopened, Now: func() time.Time { return now }}
	if err = service.Ingest(t.Context(), id, "epoch", 1, collection); err != nil {
		t.Fatal(err)
	}
	same, err := service.Read(t.Context(), tenant, "server")
	if err != nil {
		t.Fatal(err)
	}
	if same.Servers[0].Drives[0].Alerts[0].ID != episode || same.Servers[0].Drives[0].Health != "critical" {
		t.Fatal("restart or replay lost durable hardware episode")
	}
	tenants, err := reopened.ListTenants(t.Context(), "", 100)
	if err != nil {
		t.Fatal(err)
	}
	registered := false
	for _, scope := range tenants {
		if scope == tenant {
			registered = true
		}
	}
	if !registered {
		t.Fatal("restart cannot discover the ingested tenant for notification retry")
	}
	now = now.Add(time.Minute)
	passed = true
	collection.ObservedAt = now
	collection.Drives[0].ObservedAt = now
	if err = service.Ingest(t.Context(), id, "epoch", 2, collection); err != nil {
		t.Fatal(err)
	}
	recovered, err := service.Read(t.Context(), tenant, "server")
	if err != nil {
		t.Fatal(err)
	}
	a := recovered.Servers[0].Drives[0].Alerts[0]
	if a.ID != episode || a.ResolvedAt == nil || recovered.Servers[0].Drives[0].Health != "healthy" {
		t.Fatal("fresh recovery did not close the persisted episode")
	}
	notifier := &driveDelivered{fail: true}
	if err = reopened.Deliver(t.Context(), tenant, notifier); err == nil {
		t.Fatal("failed notifier was treated as delivery")
	}
	notifier.fail = false
	// Expire only this disposable fixture's lease to exercise restart retry
	// without sleeping for the operational retry interval.
	if _, err = database.DB.ExecContext(t.Context(), `UPDATE guard_drive_health_outbox SET lease_until=now()-interval '1 second' WHERE tenant_id=$1`, tenant); err != nil {
		t.Fatal(err)
	}
	if err = store.Deliver(t.Context(), tenant, notifier); err != nil {
		t.Fatal(err)
	}
	if err = reopened.Deliver(t.Context(), tenant, notifier); err != nil {
		t.Fatal(err)
	}
	fired, resolved := false, false
	for _, event := range notifier.events {
		if event.Labels["drive_id"] != reading.DriveID {
			t.Fatal("outbox lost hardware evidence attribution")
		}
		if event.ResolvedAt == nil {
			fired = true
		} else {
			resolved = true
		}
	}
	if !fired || !resolved {
		t.Fatal("restart did not deliver firing and recovery events")
	}
	// A second flush consumes no already-delivered event, even after reopening.
	notifier.events = nil
	if err = store.Deliver(t.Context(), tenant, notifier); err != nil {
		t.Fatal(err)
	}
	if len(notifier.events) != 0 {
		t.Fatal("durable acknowledgement redelivered an event")
	}
	foreign, err := service.Read(t.Context(), tenant+"-foreign", "server")
	if err != nil {
		t.Fatal(err)
	}
	if foreign.Servers[0].CollectionStatus != "missing" || len(foreign.Servers[0].Drives) != 0 {
		t.Fatal("foreign scoped read disclosed evidence")
	}
	// Enforce RLS with a non-owner role, even when the suite connection is privileged.
	role := "drive_rls_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quoted := pgx.Identifier{role}.Sanitize()
	if _, err = database.DB.ExecContext(t.Context(), "CREATE ROLE "+quoted+" NOLOGIN"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = database.DB.ExecContext(context.Background(), "DROP OWNED BY "+quoted)
		_, _ = database.DB.ExecContext(context.Background(), "DROP ROLE "+quoted)
	})
	var schema string
	if err = database.DB.QueryRowContext(t.Context(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if _, err = database.DB.ExecContext(t.Context(), "GRANT USAGE ON SCHEMA "+pgx.Identifier{schema}.Sanitize()+" TO "+quoted); err != nil {
		t.Fatal(err)
	}
	if _, err = database.DB.ExecContext(t.Context(), "GRANT SELECT ON guard_drive_health,guard_drive_health_outbox TO "+quoted); err != nil {
		t.Fatal(err)
	}
	tx, err := database.DB.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(t.Context(), "SET LOCAL ROLE "+quoted); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(t.Context(), "SELECT set_config('app.tenant_id',$1,true)", tenant+"-foreign"); err != nil {
		t.Fatal(err)
	}
	var hidden string
	err = tx.QueryRowContext(t.Context(), "SELECT server_id FROM guard_drive_health WHERE server_id='server' AND tenant_id=$1", tenant).Scan(&hidden)
	if err != sql.ErrNoRows {
		t.Fatalf("RLS disclosed the foreign server: %q %v", hidden, err)
	}
}
