package controlplane

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

type serviceJobFixtureStore interface {
	JobStore
	ServiceOperationJobStore
}

func TestMemoryServiceJobReservationKeepsCompleteTargetFence(t *testing.T) {
	testServiceJobReservation(t, NewMemoryStore())
}

func testServiceJobReservation(t *testing.T, store serviceJobFixtureStore) {
	t.Helper()
	ctx := t.Context()
	create := func(req UpsertJobRequest) {
		t.Helper()
		if _, err := store.CreateJob(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	request := func(id, tenant, stack, service string) UpsertJobRequest {
		return UpsertJobRequest{ID: id, TenantID: tenant, StackID: stack, Type: "stackkit_lifecycle", State: "pending", Payload: map[string]any{"service_id": service}}
	}
	old := request("older-unknown", "tenant-a", "stack-a", "ha")
	old.State, old.Result = "failed", map[string]any{"connector_outcome_unresolved": true}
	create(old)
	// Regression stimulus: presentation history cannot be an admission fence.
	for i := 0; i < 125; i++ {
		row := request(fmt.Sprintf("history-%d", i), "tenant-a", "stack-a", "other")
		row.State = "completed"
		create(row)
	}
	conflicting := request("conflicting-targets", "tenant-a", "stack-a", "ha")
	conflicting.Result = map[string]any{"service_action_receipt": map[string]any{"service_id": "foreign-service"}}
	if _, err := store.CreateServiceOperationJob(ctx, conflicting, "ha"); err == nil {
		t.Fatal("conflicting target escaped service reservation scope")
	}
	proposed := request("next-read", "tenant-a", "stack-a", "ha")
	if _, err := store.CreateServiceOperationJob(ctx, proposed, "ha"); !errors.Is(err, ErrServiceOperationBusy) {
		t.Fatalf("uncertain operation did not fence new reservation: %v", err)
	}
	if _, err := store.GetJob(ctx, "tenant-a", proposed.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refused reservation persisted: %v", err)
	}
	if id, err := store.FindUnresolvedServiceJob(ctx, "tenant-a", "stack-a", "ha", old.ID); err != nil || id != "" {
		t.Fatalf("selected read exclusion failed: %q %v", id, err)
	}
	for _, scope := range []struct{ tenant, stack, service string }{{"tenant-b", "stack-a", "ha"}, {"tenant-a", "stack-b", "ha"}, {"tenant-a", "stack-a", "other"}} {
		req := request("independent-"+scope.tenant+scope.stack+scope.service, scope.tenant, scope.stack, scope.service)
		if _, err := store.CreateServiceOperationJob(ctx, req, scope.service); err != nil {
			t.Fatalf("unrelated scope refused: %v", err)
		}
	}
	pending := request("payload-before-receipt", "tenant-a", "stack-a", "pending-service")
	create(pending)
	if id, err := store.FindUnresolvedServiceJob(ctx, "tenant-a", "stack-a", "pending-service", ""); err != nil || id != pending.ID {
		t.Fatalf("original pending payload target was lost: %q %v", id, err)
	}
	receipt := request("receipt-only", "tenant-a", "stack-a", "unused")
	receipt.Payload = nil
	receipt.Result = map[string]any{"service_action_receipt": map[string]any{"service_id": "receipt-service"}}
	create(receipt)
	if id, err := store.FindUnresolvedServiceJob(ctx, "tenant-a", "stack-a", "receipt-service", ""); err != nil || id != receipt.ID {
		t.Fatalf("receipt target was lost: %q %v", id, err)
	}
	first, second := request("race-first", "tenant-a", "stack-a", "race-service"), request("race-second", "tenant-a", "stack-a", "race-service")
	start := make(chan struct{})
	var wg sync.WaitGroup
	var firstErr, secondErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, firstErr = store.CreateServiceOperationJob(context.Background(), first, "race-service")
	}()
	go func() {
		defer wg.Done()
		<-start
		_, secondErr = store.CreateServiceOperationJob(context.Background(), second, "race-service")
	}()
	close(start)
	wg.Wait()
	if !((firstErr == nil && errors.Is(secondErr, ErrServiceOperationBusy)) || (secondErr == nil && errors.Is(firstErr, ErrServiceOperationBusy))) {
		t.Fatalf("concurrent service reservations escaped atomic admission: %v / %v", firstErr, secondErr)
	}
	winner := first
	if firstErr != nil {
		winner = second
	}
	if _, err := store.CreateServiceOperationJob(ctx, winner, "race-service"); !errors.Is(err, ErrConflict) {
		t.Fatalf("same operation lost its existing reservation: %v", err)
	}
	for _, scope := range []struct{ tenant, stack, service string }{{"tenant-b", "stack-a", "race-service"}, {"tenant-a", "stack-b", "race-service"}, {"tenant-a", "stack-a", "foreign-service"}} {
		if _, err := store.GetServiceOperationJob(ctx, scope.tenant, scope.stack, scope.service, winner.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("foreign service recovery returned payload: %v", err)
		}
	}
	retained, err := store.GetServiceOperationJob(ctx, winner.TenantID, winner.StackID, "race-service", winner.ID)
	if err != nil || retained.Payload["service_id"] != "race-service" {
		t.Fatalf("reserved target was overwritten: %v", err)
	}
}
