package controlplane

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestMemoryStoreWorkerIdentityIncludesTenant(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)
	store.SetNow(func() time.Time { return now })

	for _, worker := range []Worker{
		{
			ID: "shared-worker", TenantID: "tenant-a", Hostname: "cloud-a",
			IP: "192.0.2.10", OwnerSubjectID: "owner-a",
		},
		{
			ID: "shared-worker", TenantID: "tenant-b", Hostname: "basement-b",
			IP: "192.0.2.20", OwnerSubjectID: "owner-b",
		},
	} {
		if _, err := store.UpsertWorkerHeartbeat(ctx, worker); err != nil {
			t.Fatalf("upsert %s: %v", worker.TenantID, err)
		}
	}

	workerA, err := store.GetWorker(ctx, "tenant-a", "shared-worker")
	if err != nil {
		t.Fatalf("get tenant-a worker: %v", err)
	}
	workerB, err := store.GetWorker(ctx, "tenant-b", "shared-worker")
	if err != nil {
		t.Fatalf("get tenant-b worker: %v", err)
	}
	if workerA.IP != "192.0.2.10" || workerB.IP != "192.0.2.20" {
		t.Fatalf("same worker id crossed tenant boundary: tenant-a=%+v tenant-b=%+v", workerA, workerB)
	}

	approvedAt := now.Add(time.Minute)
	if _, err := store.ApproveWorker(ctx, "tenant-a", "shared-worker", "owner-a", approvedAt); err != nil {
		t.Fatalf("approve tenant-a worker: %v", err)
	}
	workerB, err = store.GetWorker(ctx, "tenant-b", "shared-worker")
	if err != nil {
		t.Fatalf("get tenant-b worker after tenant-a approval: %v", err)
	}
	if workerB.Approved {
		t.Fatalf("tenant-a approval leaked into tenant-b worker: %+v", workerB)
	}
}

func TestMemoryGuardSatelliteWorkerIdentityIncludesTenant(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewMemoryStore()
	observedAt := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)

	type fixture struct {
		tenantID string
		serverID string
		ip       string
	}
	fixtures := []fixture{
		{tenantID: "tenant-a", serverID: "server-a", ip: "192.0.2.10"},
		{tenantID: "tenant-b", serverID: "server-b", ip: "192.0.2.20"},
	}
	for _, item := range fixtures {
		server := ServerRuntime{
			ID: item.serverID, TenantID: item.tenantID, Generation: 1,
			SourceAuthority: ServerEventAuthorityGuard, SourceID: "shared-guard",
			SourceEpoch: "epoch-1", SourceSequence: 1, SourceObservedAt: &observedAt,
			InventoryRevision: 1, LifecycleState: "active",
		}
		store.mu.Lock()
		store.servers[server.ID] = server
		store.mu.Unlock()

		result, err := store.ApplyGuardInventorySatellites(ctx, GuardInventorySatelliteProjection{
			TenantID: item.tenantID, ServerID: item.serverID, Generation: 1,
			SourceID: "shared-guard", SourceEpoch: "epoch-1", SourceSequence: 1,
			SourceObservedAt: observedAt, InventoryRevision: 1,
			Worker: Worker{
				ID: "shared-guard", TenantID: item.tenantID, Hostname: item.serverID,
				IP: item.ip, LastSeenAt: &observedAt,
			},
			RILServer: RILServer{
				ID: item.serverID, TenantID: item.tenantID, Status: "healthy",
				LastSeenAt: &observedAt,
			},
		})
		if err != nil || result == nil || !result.Applied {
			t.Fatalf("apply %s Guard satellites: result=%+v err=%v", item.tenantID, result, err)
		}
	}

	for _, item := range fixtures {
		worker, err := store.GetWorker(ctx, item.tenantID, "shared-guard")
		if err != nil {
			t.Fatalf("get %s Guard worker: %v", item.tenantID, err)
		}
		if worker.IP != item.ip {
			t.Fatalf("%s Guard worker IP = %q, want %q", item.tenantID, worker.IP, item.ip)
		}
	}
}

func TestPostgresStoreWorkerUpsertConflictsOnTenantAndID(t *testing.T) {
	t.Parallel()

	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	store := NewPostgresStore(db)
	now := time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	expectTenantGUC(mock, "tenant-a")
	mock.ExpectQuery(
		"(?s)" + regexp.QuoteMeta("INSERT INTO workers") + ".*" +
			regexp.QuoteMeta("ON CONFLICT (tenant_id, id) DO UPDATE"),
	).WillReturnRows(workerRows().AddRow(
		"shared-worker", "tenant-a", nil, nil, "cloud-a", "192.0.2.10",
		"linux", "amd64", nil, "pending", false, nil, now,
		4, 8192, 100, nil, false, false, "26.0", "agent", "self-hosted",
		`{}`, "owner-a", `{}`, `{}`, now, now,
	))
	mock.ExpectCommit()

	worker, err := store.UpsertWorkerHeartbeat(context.Background(), Worker{
		ID: "shared-worker", TenantID: "tenant-a", Hostname: "cloud-a",
		IP: "192.0.2.10", OS: "linux", Arch: "amd64", Status: "pending",
		LastSeenAt: &now, CPUCores: 4, RAMMB: 8192, DiskGB: 100,
		DockerVersion: "26.0", Type: "agent", Provider: "self-hosted",
		OwnerSubjectID: "owner-a",
	})
	if err != nil {
		t.Fatalf("UpsertWorkerHeartbeat: %v", err)
	}
	if worker.TenantID != "tenant-a" || worker.ID != "shared-worker" {
		t.Fatalf("unexpected worker: %+v", worker)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
