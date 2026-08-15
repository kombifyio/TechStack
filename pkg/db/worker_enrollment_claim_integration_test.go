package db

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/google/uuid"
)

func TestIntegrationWorkerEnrollmentClaimHasOneImmutableWinner(t *testing.T) {
	database := openTestDB(t)
	if err := database.Migrate(t.Context()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	store := controlplane.NewPostgresStore(database.DB)
	suffix := uuid.NewString()
	tenantID := "enrollment-tenant-" + suffix
	workerID := "enrollment-worker-" + suffix
	claims := []controlplane.WorkerEnrollmentClaim{
		integrationWorkerEnrollmentClaim(tenantID, workerID, "server-a-"+suffix),
		integrationWorkerEnrollmentClaim(tenantID, workerID, "server-b-"+suffix),
	}
	type outcome struct {
		index  int
		result *controlplane.WorkerEnrollmentClaimResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, len(claims))
	var group sync.WaitGroup
	for index := range claims {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			result, err := store.ClaimWorkerEnrollment(t.Context(), claims[index])
			outcomes <- outcome{index: index, result: result, err: err}
		}(index)
	}
	close(start)
	group.Wait()
	close(outcomes)

	winner := -1
	conflicts := 0
	for result := range outcomes {
		switch {
		case result.err == nil:
			if winner != -1 {
				t.Fatal("two different Postgres enrollment bindings succeeded")
			}
			winner = result.index
			if result.result == nil || result.result.Worker == nil || !result.result.Created {
				t.Fatalf("winning Postgres claim = %#v", result.result)
			}
		case errors.Is(result.err, controlplane.ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected Postgres claim error: %v", result.err)
		}
	}
	if winner == -1 || conflicts != 1 {
		t.Fatalf("winner=%d conflicts=%d", winner, conflicts)
	}

	worker, err := store.GetWorker(t.Context(), tenantID, workerID)
	if err != nil {
		t.Fatalf("GetWorker: %v", err)
	}
	wantServerID := claims[winner].Binding.ServerID
	if worker.LastSeenAt != nil ||
		worker.StackID != "" ||
		worker.OwnerSubjectID != "owner-1" ||
		worker.Capabilities["server_id"] != wantServerID ||
		worker.Capabilities["runtime_agent_id"] != workerID {
		t.Fatalf("persisted Postgres enrollment = %#v, want server %q and no heartbeat", worker, wantServerID)
	}

	replayed, err := store.ClaimWorkerEnrollment(t.Context(), claims[winner])
	if err != nil {
		t.Fatalf("replay winning claim: %v", err)
	}
	if replayed == nil || replayed.Worker == nil || replayed.Created || replayed.Worker.LastSeenAt != nil {
		t.Fatalf("replayed Postgres claim = %#v", replayed)
	}
}

func integrationWorkerEnrollmentClaim(tenantID, workerID, serverID string) controlplane.WorkerEnrollmentClaim {
	approvedAt := time.Date(2026, 7, 23, 16, 0, 0, 0, time.UTC)
	return controlplane.WorkerEnrollmentClaim{
		Binding: controlplane.WorkerEnrollmentBinding{
			TenantID: tenantID, WorkerID: workerID, OwnerSubjectID: "owner-1",
			ServerID: serverID, RuntimeAgentID: workerID,
		},
		Worker: controlplane.Worker{
			ID: workerID, TenantID: tenantID, OwnerSubjectID: "owner-1",
			Hostname: serverID, OS: "ubuntu", Arch: "amd64", Status: "pending",
			Approved: true, ApprovedAt: &approvedAt, Type: "runtime", Provider: "local",
			Capabilities: map[string]any{
				"server_id": serverID, "runtime_agent_id": workerID,
			},
		},
	}
}
