package main

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/orchestrator"
)

func TestRuntimeShutdownSequenceKeepsProviderPoolUntilEveryMutationProducerDrains(t *testing.T) {
	order := make([]string, 0, 5)
	providerLifecycle := startProviderControlLifecycle(context.Background(), func(ctx context.Context) {
		<-ctx.Done()
		order = append(order, "provider-worker-joined")
	})

	runRuntimeShutdownSequence(
		providerLifecycle,
		func() { order = append(order, "orchestrator-drained") },
		func() { order = append(order, "provider-database-closed") },
		func() { order = append(order, "remaining-runtime-stopped") },
		func() { order = append(order, "control-plane-database-closed") },
	)

	want := []string{
		"provider-worker-joined",
		"orchestrator-drained",
		"provider-database-closed",
		"remaining-runtime-stopped",
		"control-plane-database-closed",
	}
	if !slices.Equal(order, want) {
		t.Fatalf("shutdown order = %v, want %v", order, want)
	}
}

func TestStopRuntimeLifecycleWaitsForProviderControlBeforeDatabaseClose(t *testing.T) {
	controlPlaneSQL, controlPlaneMock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	controlPlaneMock.ExpectPing()
	controlPlaneMock.ExpectClose()
	providerSQL, providerMock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("provider sqlmock.New: %v", err)
	}
	providerMock.ExpectPing()
	providerMock.ExpectClose()

	cancelObserved := make(chan struct{})
	releaseProvider := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseProvider) }) }
	defer release()
	providerLifecycle := startProviderControlLifecycle(context.Background(), func(ctx context.Context) {
		<-ctx.Done()
		close(cancelObserved)
		<-releaseProvider
	})
	handles := &shutdownHandles{providerControl: providerLifecycle}
	providerDatabase := &db.DB{DB: providerSQL}
	v2State := &v2Boot{db: &db.DB{DB: controlPlaneSQL}}
	orch := orchestrator.New(nil, &orchestrator.Config{Workers: 0}, nil)

	stopped := make(chan struct{})
	go func() {
		stopRuntimeLifecycle(orch, &grpcBoot{}, nil, &monitoringBoot{}, providerDatabase, v2State, handles)
		close(stopped)
	}()

	awaitLifecycleSignal(t, cancelObserved, "provider-control cancellation")
	if err := providerSQL.PingContext(context.Background()); err != nil {
		t.Fatalf("provider database closed before provider control drained: %v", err)
	}
	if err := controlPlaneSQL.PingContext(context.Background()); err != nil {
		t.Fatalf("control-plane database closed before provider control drained: %v", err)
	}
	release()
	awaitLifecycleSignal(t, stopped, "runtime shutdown")
	if err := providerMock.ExpectationsWereMet(); err != nil {
		t.Fatalf("provider shutdown expectations: %v", err)
	}
	if err := controlPlaneMock.ExpectationsWereMet(); err != nil {
		t.Fatalf("control-plane shutdown expectations: %v", err)
	}
}

func awaitLifecycleSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}
