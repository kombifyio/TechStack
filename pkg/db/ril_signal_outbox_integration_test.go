package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/ril/signals"
	"github.com/google/uuid"
)

func TestIntegrationRILSignalOutboxDedupeAndTenantServerFence(t *testing.T) {
	dsn := integrationDSN()
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping Postgres integration test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	database, err := Open(Config{Backend: StoreBackendPostgres, DSN: dsn, MaxOpenConns: 4, MaxIdleConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()
	tenantA, tenantB, serverID := "signal-tenant-a-"+suffix, "signal-tenant-b-"+suffix, "signal-server-"+suffix
	if _, err := database.ExecContext(ctx, `
		INSERT INTO techstack_tenants (id, display_name) VALUES ($1,'Signal tenant A'),($2,'Signal tenant B')
	`, tenantA, tenantB); err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantA); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO servers (id, tenant_id, owner_subject_id, name)
		VALUES ($1,$2,'owner-1','Signal server')
	`, serverID, tenantA); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	outbox := signals.NewPostgresOutbox(database.DB)
	observation := signals.Observation{
		DedupeKey: "health:" + serverID, TenantID: tenantA, ServerID: serverID,
		Source: signals.SourceHealth, Severity: signals.SeverityHigh,
		TraceID: "trace-" + suffix, AuditID: "audit-" + suffix, ReceivedAt: time.Now().UTC(),
	}
	first, inserted, err := outbox.Emit(ctx, observation)
	if err != nil || !inserted {
		t.Fatalf("first Emit = inserted:%t error:%v", inserted, err)
	}
	replay, inserted, err := outbox.Emit(ctx, observation)
	if err != nil || inserted || replay.SequenceID != first.SequenceID || replay.Envelope.SignalID != first.Envelope.SignalID {
		t.Fatalf("dedupe Emit = record:%+v inserted:%t error:%v", replay, inserted, err)
	}
	conflict := observation
	conflict.Severity = signals.SeverityCritical
	if _, _, err := outbox.Emit(ctx, conflict); !errors.Is(err, signals.ErrDedupeConflict) {
		t.Fatalf("changed dedupe payload error = %v, want ErrDedupeConflict", err)
	}

	unauthorized := observation
	unauthorized.TenantID = tenantB
	unauthorized.TraceID = "trace-b-" + suffix
	unauthorized.AuditID = "audit-b-" + suffix
	if _, _, err := outbox.Emit(ctx, unauthorized); !errors.Is(err, signals.ErrServerUnauthorized) {
		t.Fatalf("cross-tenant Emit error = %v, want ErrServerUnauthorized", err)
	}
	wrongOwner := observation
	wrongOwner.DedupeKey = "health:wrong-owner:" + serverID
	wrongOwner.UserID = "auth0|other-owner"
	if _, _, err := outbox.Emit(ctx, wrongOwner); !errors.Is(err, signals.ErrServerUnauthorized) {
		t.Fatalf("cross-owner Emit error = %v, want ErrServerUnauthorized", err)
	}
}
