package controlplane

import (
	"context"
	"testing"
	"time"
)

func TestMemoryGuardInventorySatellitesBlockedOlderWriteCannotRollbackNewHead(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	observedN := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	observedN1 := observedN.Add(time.Second)
	server := ServerRuntime{
		ID: "server-1", TenantID: "tenant-1", Generation: 4,
		SourceAuthority: ServerEventAuthorityGuard, SourceID: "guard-1", SourceEpoch: "epoch-1",
		SourceSequence: 2, SourceObservedAt: &observedN1, InventoryRevision: 2,
		LifecycleState: "active",
	}
	store.mu.Lock()
	store.servers[server.ID] = server
	store.mu.Unlock()

	command := func(sequence, revision int64, observedAt time.Time, ip, status string) GuardInventorySatelliteProjection {
		return GuardInventorySatelliteProjection{
			TenantID: "tenant-1", ServerID: "server-1", Generation: 4,
			SourceID: "guard-1", SourceEpoch: "epoch-1", SourceSequence: sequence,
			SourceObservedAt: observedAt, InventoryRevision: revision,
			Worker:    Worker{ID: "guard-1", TenantID: "tenant-1", IP: ip, LastSeenAt: &observedAt},
			RILServer: RILServer{ID: "server-1", TenantID: "tenant-1", Status: status, LastSeenAt: &observedAt},
		}
	}

	blocked := make(chan struct{})
	release := make(chan struct{})
	olderDone := make(chan *GuardInventorySatelliteResult, 1)
	go func() {
		close(blocked)
		<-release
		result, err := store.ApplyGuardInventorySatellites(context.Background(), command(1, 1, observedN, "192.0.2.1", "degraded"))
		if err != nil {
			t.Errorf("apply blocked N satellites: %v", err)
		}
		olderDone <- result
	}()
	<-blocked

	newer, err := store.ApplyGuardInventorySatellites(context.Background(), command(2, 2, observedN1, "192.0.2.2", "healthy"))
	if err != nil || newer == nil || !newer.Applied {
		t.Fatalf("apply N+1 satellites: result=%+v err=%v", newer, err)
	}
	close(release)
	older := <-olderDone
	if older == nil || older.Applied {
		t.Fatalf("blocked N must become a stale no-op, got %+v", older)
	}

	worker, err := store.GetWorker(context.Background(), "tenant-1", "guard-1")
	if err != nil || worker.IP != "192.0.2.2" {
		t.Fatalf("worker rolled back: worker=%+v err=%v", worker, err)
	}
	ril, err := store.GetRILServer(context.Background(), "tenant-1", "server-1")
	if err != nil || ril.Status != "healthy" {
		t.Fatalf("RIL server rolled back: server=%+v err=%v", ril, err)
	}
}
