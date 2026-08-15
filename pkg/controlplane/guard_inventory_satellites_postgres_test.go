package controlplane

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPostgresGuardSatelliteWorkerUpsertConflictsOnTenantAndID(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	observedAt := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-a")
	mock.ExpectQuery("(?s)"+regexp.QuoteMeta("SELECT 1")+".*"+regexp.QuoteMeta("FROM servers")+".*"+regexp.QuoteMeta("FOR SHARE")).
		WithArgs("tenant-a", "server-a", int64(1), "shared-guard", "epoch-1", int64(1), int64(1), observedAt).
		WillReturnRows(sqlmock.NewRows([]string{"marker"}).AddRow(1))
	mock.ExpectExec(
		"(?s)" + regexp.QuoteMeta("INSERT INTO workers") + ".*" +
			regexp.QuoteMeta("ON CONFLICT (tenant_id, id) DO UPDATE"),
	).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO ril_servers")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(
		"(?s)"+regexp.QuoteMeta("SELECT id, tenant_id")+".*"+
			regexp.QuoteMeta("FROM workers WHERE tenant_id = $1 AND id = $2"),
	).WithArgs("tenant-a", "shared-guard").
		WillReturnRows(workerRows().AddRow(
			"shared-guard", "tenant-a", nil, nil, "server-a", "192.0.2.10",
			"linux", "amd64", nil, "connected", false, nil, observedAt,
			4, 8192, 100, nil, false, false, "26.0", "agent", "self-hosted",
			`{}`, "owner-a", `{}`, `{}`, observedAt, observedAt,
		))
	mock.ExpectCommit()

	result, err := store.ApplyGuardInventorySatellites(context.Background(), GuardInventorySatelliteProjection{
		TenantID: "tenant-a", ServerID: "server-a", Generation: 1,
		SourceID: "shared-guard", SourceEpoch: "epoch-1", SourceSequence: 1,
		SourceObservedAt: observedAt, InventoryRevision: 1,
		Worker: Worker{
			ID: "shared-guard", TenantID: "tenant-a", Hostname: "server-a",
			IP: "192.0.2.10", OS: "linux", Arch: "amd64", Status: "connected",
			LastSeenAt: &observedAt, CPUCores: 4, RAMMB: 8192, DiskGB: 100,
			DockerVersion: "26.0", Type: "agent", Provider: "self-hosted",
			OwnerSubjectID: "owner-a",
		},
		RILServer: RILServer{
			ID: "server-a", TenantID: "tenant-a", Name: "server-a",
			Status: "healthy", LastSeenAt: &observedAt,
		},
	})
	if err != nil {
		t.Fatalf("ApplyGuardInventorySatellites: %v", err)
	}
	if result == nil || !result.Applied || result.Worker == nil {
		t.Fatalf("unexpected result: %+v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
