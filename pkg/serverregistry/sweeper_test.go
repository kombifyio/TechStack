package serverregistry_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

type sweepClock struct{ now time.Time }

func (c *sweepClock) Now() time.Time { return c.now }

func newSweepFixture(t *testing.T) (*controlplane.MemoryStore, *serverregistry.Sweeper, *sweepClock) {
	t.Helper()
	store := controlplane.NewMemoryStore()
	clock := &sweepClock{now: time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)}
	store.SetNow(clock.Now)
	sweeper, err := serverregistry.NewSweeper(serverregistry.SweeperConfig{
		Registry: store,
		Outbox:   store,
		Now:      clock.Now,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		IsBenignConflict: func(err error) bool {
			return errors.Is(err, controlplane.ErrConflict)
		},
		OnError: func(err error) { t.Fatalf("sweep error: %v", err) },
	})
	if err != nil {
		t.Fatalf("NewSweeper: %v", err)
	}
	return store, sweeper, clock
}

func enrollSweepServer(t *testing.T, store *controlplane.MemoryStore, observedAt time.Time) {
	t.Helper()
	if _, err := store.ApplyServerEvent(context.Background(), controlplane.ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1",
		Authority: controlplane.ServerEventAuthorityControlPlane,
		Source:    "enrollment", SourceID: "enrollment", ObservedAt: observedAt,
		Runtime: controlplane.ServerRuntime{
			ID: "server-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1",
			WorkerID: "guard-1", NodeID: "server-1", Name: "runtime-1",
			LifecycleState: string(serverregistry.LifecycleEnrolling),
			DesiredState:   string(serverregistry.DesiredRunning),
		},
	}); err != nil {
		t.Fatalf("enroll server: %v", err)
	}
}

func applySweepHeartbeat(t *testing.T, store *controlplane.MemoryStore, heartbeatAt time.Time, sequence int64) *controlplane.ServerEventResult {
	t.Helper()
	current, err := store.GetServerRuntime(context.Background(), "tenant-1", "server-1")
	if err != nil {
		t.Fatalf("get server: %v", err)
	}
	connection, health := serverregistry.DeriveObservedState(heartbeatAt, &heartbeatAt, "")
	result, err := store.ApplyServerEvent(context.Background(), controlplane.ServerEvent{
		TenantID: "tenant-1", ServerID: "server-1",
		ExpectedRevision: current.Revision, Generation: current.Generation,
		Authority: controlplane.ServerEventAuthorityGuard,
		Source:    "guard-heartbeat", SourceID: "guard-1",
		SourceEpoch: "epoch-a", SourceSequence: sequence, ObservedAt: heartbeatAt,
		ClearConnectionReason: true, ClearHealthReason: true,
		Runtime: controlplane.ServerRuntime{
			ConnectionState: string(connection), HealthState: string(health),
			LastHeartbeatAt: &heartbeatAt,
		},
	})
	if err != nil {
		t.Fatalf("apply heartbeat seq %d: %v", sequence, err)
	}
	return result
}

func sweepTransitionCount(t *testing.T, store *controlplane.MemoryStore) int {
	t.Helper()
	transitions, err := store.ListServerTransitions(context.Background(), "tenant-1", "server-1", 100)
	if err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	return len(transitions)
}

// TestSweeperDemotionMatrix locks the K2 acceptance semantics: fresh
// heartbeats are untouched, stale heartbeats demote once to stale/unknown,
// expired heartbeats demote once to offline/unknown, and repeated sweeps never
// mint no-op transitions.
func TestSweeperDemotionMatrix(t *testing.T) {
	store, sweeper, clock := newSweepFixture(t)
	ctx := context.Background()
	base := clock.now
	enrollSweepServer(t, store, base)
	applySweepHeartbeat(t, store, base, 1)

	// Persisted state equals the runtimehealth recompute under a fresh
	// heartbeat: DeriveObservedState stays a test-only cross-check now that
	// read paths return persisted state.
	server, err := store.GetServerRuntime(ctx, "tenant-1", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	derivedConnection, derivedHealth := serverregistry.DeriveObservedState(base.Add(30*time.Second), server.LastHeartbeatAt, "")
	if server.ConnectionState != string(derivedConnection) || server.HealthState != string(derivedHealth) {
		t.Fatalf("persisted state %s/%s diverges from recompute %s/%s", server.ConnectionState, server.HealthState, derivedConnection, derivedHealth)
	}

	// Fresh window: no demotion.
	clock.now = base.Add(30 * time.Second)
	result, err := sweeper.SweepOnce(ctx)
	if err != nil {
		t.Fatalf("fresh sweep: %v", err)
	}
	if result.Demotions != 0 {
		t.Fatalf("fresh heartbeat was demoted: %#v", result)
	}
	transitionsAfterFresh := sweepTransitionCount(t, store)

	// Stale window (>90s): one demotion writing connection+health transitions.
	clock.now = base.Add(2 * time.Minute)
	result, err = sweeper.SweepOnce(ctx)
	if err != nil {
		t.Fatalf("stale sweep: %v", err)
	}
	if result.Demotions != 1 {
		t.Fatalf("stale sweep demotions = %d, want 1 (%#v)", result.Demotions, result)
	}
	server, err = store.GetServerRuntime(ctx, "tenant-1", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	if server.ConnectionState != string(serverregistry.ConnectionStale) ||
		server.HealthState != string(serverregistry.HealthUnknown) ||
		server.ConnectionReasonCode != serverregistry.ReasonHeartbeatStale {
		t.Fatalf("stale demotion state = %s/%s reason=%s", server.ConnectionState, server.HealthState, server.ConnectionReasonCode)
	}
	if server.LastHeartbeatAt == nil || !server.LastHeartbeatAt.Equal(base) {
		t.Fatalf("sweeper rewrote LastHeartbeatAt: %#v", server.LastHeartbeatAt)
	}
	transitionsAfterStale := sweepTransitionCount(t, store)
	if transitionsAfterStale != transitionsAfterFresh+2 {
		t.Fatalf("stale demotion transitions = %d, want %d", transitionsAfterStale, transitionsAfterFresh+2)
	}

	// Same window again: no-op suppressed, no new transitions, no revision.
	revisionAfterStale := server.Revision
	result, err = sweeper.SweepOnce(ctx)
	if err != nil {
		t.Fatalf("repeat stale sweep: %v", err)
	}
	if result.Demotions != 0 || sweepTransitionCount(t, store) != transitionsAfterStale {
		t.Fatalf("repeated sweep minted no-op transitions: %#v", result)
	}
	server, _ = store.GetServerRuntime(ctx, "tenant-1", "server-1")
	if server.Revision != revisionAfterStale {
		t.Fatalf("repeated sweep advanced revision %d -> %d", revisionAfterStale, server.Revision)
	}

	// Offline window (>5m): stale -> offline written once.
	clock.now = base.Add(6 * time.Minute)
	result, err = sweeper.SweepOnce(ctx)
	if err != nil {
		t.Fatalf("offline sweep: %v", err)
	}
	if result.Demotions != 1 {
		t.Fatalf("offline sweep demotions = %d, want 1", result.Demotions)
	}
	server, _ = store.GetServerRuntime(ctx, "tenant-1", "server-1")
	if server.ConnectionState != string(serverregistry.ConnectionOffline) ||
		server.HealthState != string(serverregistry.HealthUnknown) ||
		server.ConnectionReasonCode != serverregistry.ReasonHeartbeatExpired {
		t.Fatalf("offline demotion state = %s/%s reason=%s", server.ConnectionState, server.HealthState, server.ConnectionReasonCode)
	}
	transitionsAfterOffline := sweepTransitionCount(t, store)
	if result, err = sweeper.SweepOnce(ctx); err != nil || result.Demotions != 0 || sweepTransitionCount(t, store) != transitionsAfterOffline {
		t.Fatalf("repeated offline sweep was not a no-op: %#v err=%v", result, err)
	}
}

// TestSweeperGuardRecoveryAfterDemotion documents the sequence trade-off: the
// sweeper extends the Guard's admission position, so a resumed Guard loses at
// most the demotion count in heartbeats before promoting back to connected.
func TestSweeperGuardRecoveryAfterDemotion(t *testing.T) {
	store, sweeper, clock := newSweepFixture(t)
	ctx := context.Background()
	base := clock.now
	enrollSweepServer(t, store, base)
	applySweepHeartbeat(t, store, base, 1)

	clock.now = base.Add(2 * time.Minute)
	if _, err := sweeper.SweepOnce(ctx); err != nil {
		t.Fatal(err)
	}
	clock.now = base.Add(6 * time.Minute)
	if _, err := sweeper.SweepOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// Sweeper consumed sequences 2 (stale) and 3 (offline). The Guard's local
	// counter resumes at 2: superseded observations are dropped as unapplied
	// replays, and the first sequence past the sweeper's bumps promotes the
	// server back to connected.
	recoveredAt := base.Add(7 * time.Minute)
	if result := applySweepHeartbeat(t, store, recoveredAt, 2); result.Applied {
		t.Fatal("superseded guard sequence 2 was applied over the sweeper bump")
	}
	if result := applySweepHeartbeat(t, store, recoveredAt.Add(30*time.Second), 3); result.Applied {
		t.Fatal("superseded guard sequence 3 was applied over the sweeper bump")
	}
	promoted := applySweepHeartbeat(t, store, recoveredAt.Add(time.Minute), 4)
	if !promoted.Applied {
		t.Fatal("guard sequence 4 was not accepted after sweeper demotions")
	}
	server, err := store.GetServerRuntime(ctx, "tenant-1", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	if server.ConnectionState != string(serverregistry.ConnectionConnected) ||
		server.HealthState != string(serverregistry.HealthHealthy) ||
		server.ConnectionReasonCode != "" {
		t.Fatalf("guard recovery did not promote server: %s/%s reason=%s", server.ConnectionState, server.HealthState, server.ConnectionReasonCode)
	}
}

// TestSweeperDemotionIsCASFencedAcrossInstances proves multi-instance safety:
// a second instance holding the same observed revision loses the
// compare-and-swap and writes nothing.
func TestSweeperDemotionIsCASFencedAcrossInstances(t *testing.T) {
	store, _, clock := newSweepFixture(t)
	ctx := context.Background()
	base := clock.now
	enrollSweepServer(t, store, base)
	applySweepHeartbeat(t, store, base, 1)

	now := base.Add(2 * time.Minute)
	clock.now = now
	observed, err := store.GetServerRuntime(ctx, "tenant-1", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	first, firstDue := serverregistry.DemotionCommand(now, *observed)
	second, secondDue := serverregistry.DemotionCommand(now, *observed)
	if !firstDue || !secondDue {
		t.Fatal("stale server was not due for demotion")
	}
	if _, err := store.ApplyServerEvent(ctx, first); err != nil {
		t.Fatalf("first instance demotion: %v", err)
	}
	transitions := sweepTransitionCount(t, store)
	if _, err := store.ApplyServerEvent(ctx, second); !errors.Is(err, controlplane.ErrConflict) {
		t.Fatalf("second instance demotion error = %v, want CAS conflict", err)
	}
	if sweepTransitionCount(t, store) != transitions {
		t.Fatal("losing instance wrote transitions")
	}
}

// TestSweeperSkipsAggregatesWithoutGuardCheckpoint: rows without a Guard
// source position (legacy projections, freshly fenced generations) have no
// admission position to extend and must be left alone.
func TestSweeperSkipsAggregatesWithoutGuardCheckpoint(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	heartbeat := now.Add(-10 * time.Minute)
	server := controlplane.ServerRuntime{
		ID: "server-legacy", TenantID: "tenant-1", WorkerID: "guard-1",
		ConnectionState: string(serverregistry.ConnectionConnected),
		HealthState:     string(serverregistry.HealthHealthy),
		LastHeartbeatAt: &heartbeat,
		LifecycleState:  string(serverregistry.LifecycleActive),
	}
	if _, due := serverregistry.DemotionCommand(now, server); due {
		t.Fatal("aggregate without a guard source epoch was demoted")
	}
	server.SourceEpoch = "epoch-a"
	server.SourceID = "guard-1"
	server.SourceAuthority = serverregistry.AuthorityGuard
	if _, due := serverregistry.DemotionCommand(now, server); !due {
		t.Fatal("guard-checkpointed stale aggregate was not demoted")
	}
	server.LifecycleState = string(serverregistry.LifecycleDecommissioning)
	if _, due := serverregistry.DemotionCommand(now, server); due {
		t.Fatal("decommissioning aggregate was demoted")
	}
}

// TestSweeperPrunesOutboxAtRetentionBoundary covers kombify-Techstack-nzy1.1:
// rows older than the retention window are deleted, newer rows survive, and
// the size gauge is recorded on every pass.
func TestSweeperPrunesOutboxAtRetentionBoundary(t *testing.T) {
	store := controlplane.NewMemoryStore()
	clock := &sweepClock{now: time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)}
	store.SetNow(clock.Now)
	var recorded []serverregistry.OutboxStats
	sweeper, err := serverregistry.NewSweeper(serverregistry.SweeperConfig{
		Registry: store,
		Outbox:   store,
		Now:      clock.Now,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		IsBenignConflict: func(err error) bool {
			return errors.Is(err, controlplane.ErrConflict)
		},
		RecordOutboxStats: func(stats serverregistry.OutboxStats) {
			recorded = append(recorded, stats)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := clock.now
	enrollSweepServer(t, store, base)
	applySweepHeartbeat(t, store, base, 1)
	oldStats, err := store.ServerRegistryOutboxStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if oldStats.EstimatedRows != 2 {
		t.Fatalf("outbox rows = %d, want 2 (enrollment + heartbeat)", oldStats.EstimatedRows)
	}

	// A fresh revision 20 days later stays inside the retention window.
	clock.now = base.Add(20 * 24 * time.Hour)
	applySweepHeartbeat(t, store, clock.now, 2)

	// 31 days after enrollment the first two rows are past retention.
	clock.now = base.Add(31 * 24 * time.Hour)
	result, err := sweeper.SweepOnce(ctx)
	if err != nil {
		t.Fatalf("prune sweep: %v", err)
	}
	if result.OutboxPruned != 2 {
		t.Fatalf("pruned rows = %d, want the 2 rows older than 30d (%#v)", result.OutboxPruned, result)
	}
	// Stats are recorded after this pass's demotion write (enrollment,
	// heartbeat 1, heartbeat 2, demotion) and before the prune.
	if len(recorded) == 0 || recorded[0].EstimatedRows != 4 {
		t.Fatalf("outbox stats gauge not recorded before prune: %#v", recorded)
	}
	stats, err := store.ServerRegistryOutboxStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The 20-day-old heartbeat row survives, plus the demotion row written by
	// this sweep pass (the ancient heartbeat is offline by now).
	if stats.EstimatedRows != 2 {
		t.Fatalf("outbox rows after prune = %d, want 2", stats.EstimatedRows)
	}
	if stats.OldestCreatedAt == nil || stats.OldestCreatedAt.Before(base.Add(20*24*time.Hour)) {
		t.Fatalf("prune removed the wrong boundary rows: oldest=%v", stats.OldestCreatedAt)
	}
	// Second pass: nothing left to prune.
	if result, err = sweeper.SweepOnce(ctx); err != nil || result.OutboxPruned != 0 {
		t.Fatalf("repeat prune = %#v err=%v, want zero", result, err)
	}
}
