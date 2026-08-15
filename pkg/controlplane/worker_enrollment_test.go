package controlplane

import (
	"errors"
	"testing"
	"time"
)

func TestMemoryWorkerEnrollmentClaimIsTenantBoundAndObservationFree(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	now := time.Date(2026, 7, 23, 15, 0, 0, 0, time.UTC)
	store.SetNow(func() time.Time { return now })
	claim := testWorkerEnrollmentClaim("tenant-1", "owner-1", "stack-1", "server-1", "runtime-1")

	first, err := store.ClaimWorkerEnrollment(t.Context(), claim)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if first == nil || first.Worker == nil || !first.Created {
		t.Fatalf("first claim result = %#v", first)
	}
	if first.Worker.LastSeenAt != nil {
		t.Fatalf("declarative enrollment invented last_seen_at: %#v", first.Worker.LastSeenAt)
	}

	replay := claim
	replay.Worker.Hostname = "changed-declaration"
	replayed, err := store.ClaimWorkerEnrollment(t.Context(), replay)
	if err != nil {
		t.Fatalf("identical binding replay: %v", err)
	}
	if replayed == nil || replayed.Worker == nil || replayed.Created {
		t.Fatalf("replayed claim result = %#v", replayed)
	}
	if replayed.Worker.Hostname != claim.Worker.Hostname || replayed.Worker.LastSeenAt != nil {
		t.Fatalf("binding replay mutated worker observation/declaration: %#v", replayed.Worker)
	}

	for name, mutate := range map[string]func(*WorkerEnrollmentClaim){
		"owner": func(candidate *WorkerEnrollmentClaim) {
			candidate.Binding.OwnerSubjectID = "owner-2"
			candidate.Worker.OwnerSubjectID = "owner-2"
		},
		"stack": func(candidate *WorkerEnrollmentClaim) {
			candidate.Binding.StackID = "stack-2"
			candidate.Worker.StackID = "stack-2"
		},
		"server": func(candidate *WorkerEnrollmentClaim) {
			candidate.Binding.ServerID = "server-2"
			candidate.Worker.Capabilities[workerEnrollmentServerIDKey] = "server-2"
		},
	} {
		t.Run(name+" conflict", func(t *testing.T) {
			candidate := testWorkerEnrollmentClaim("tenant-1", "owner-1", "stack-1", "server-1", "runtime-1")
			mutate(&candidate)
			if _, err := store.ClaimWorkerEnrollment(t.Context(), candidate); !errors.Is(err, ErrConflict) {
				t.Fatalf("different %s binding error = %v, want ErrConflict", name, err)
			}
		})
	}

	otherTenant := testWorkerEnrollmentClaim("tenant-2", "owner-2", "stack-2", "server-2", "runtime-1")
	second, err := store.ClaimWorkerEnrollment(t.Context(), otherTenant)
	if err != nil || second == nil || !second.Created {
		t.Fatalf("same worker id in another tenant: result=%#v err=%v", second, err)
	}
}

func TestWorkerEnrollmentClaimRejectsHeartbeatEvidence(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	claim := testWorkerEnrollmentClaim("tenant-1", "owner-1", "stack-1", "server-1", "runtime-1")
	now := time.Now().UTC()
	claim.Worker.LastSeenAt = &now
	if _, err := store.ClaimWorkerEnrollment(t.Context(), claim); !errors.Is(err, ErrConflict) {
		t.Fatalf("heartbeat-bearing enrollment error = %v, want ErrConflict", err)
	}
}

func testWorkerEnrollmentClaim(tenantID, ownerID, stackID, serverID, workerID string) WorkerEnrollmentClaim {
	approvedAt := time.Date(2026, 7, 23, 14, 0, 0, 0, time.UTC)
	return WorkerEnrollmentClaim{
		Binding: WorkerEnrollmentBinding{
			TenantID: tenantID, WorkerID: workerID, OwnerSubjectID: ownerID,
			StackID: stackID, ServerID: serverID, RuntimeAgentID: workerID,
		},
		Worker: Worker{
			ID: workerID, TenantID: tenantID, StackID: stackID, OwnerSubjectID: ownerID,
			Hostname: "declared-host", OS: "ubuntu", Arch: "amd64", Status: "pending",
			Approved: true, ApprovedAt: &approvedAt, Type: "runtime", Provider: "local",
			Capabilities: map[string]any{
				workerEnrollmentServerIDKey: serverID, workerEnrollmentRuntimeAgentIDKey: workerID,
			},
		},
	}
}
