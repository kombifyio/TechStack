package orchestrator

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

func storeWith(t *testing.T, workers ...controlplane.Worker) *controlplane.MemoryStore {
	t.Helper()
	store := controlplane.NewMemoryStore()
	for _, worker := range workers {
		if _, err := store.UpsertWorkerHeartbeat(context.Background(), worker); err != nil {
			t.Fatalf("seed worker: %v", err)
		}
	}
	return store
}

func TestResolveBackupAgentPicksTheApprovedOwner(t *testing.T) {
	store := storeWith(t,
		controlplane.Worker{ID: "worker-a", TenantID: "tenant-a", StackID: "stack-a", Approved: true},
		controlplane.Worker{ID: "worker-b", TenantID: "tenant-a", StackID: "stack-other", Approved: true},
	)
	o := &Orchestrator{cfg: Config{WorkerStore: store}}

	agent, err := o.resolveBackupAgent(context.Background(), "tenant-a", "stack-a")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if agent != "worker-a" {
		t.Fatalf("agent = %q, want worker-a", agent)
	}
}

// TestResolveBackupAgentIgnoresUnapprovedEnrollments keeps a backup from
// running against a machine the owner has not accepted.
func TestResolveBackupAgentIgnoresUnapprovedEnrollments(t *testing.T) {
	store := storeWith(t,
		controlplane.Worker{ID: "worker-a", TenantID: "tenant-a", StackID: "stack-a", Approved: false},
	)
	o := &Orchestrator{cfg: Config{WorkerStore: store}}

	agent, err := o.resolveBackupAgent(context.Background(), "tenant-a", "stack-a")
	if err != nil || agent != "" {
		t.Fatalf("an unapproved enrollment must not be selected: agent=%q err=%v", agent, err)
	}
}

// TestResolveBackupAgentRefusesAmbiguity covers a control-plane inconsistency:
// picking one of two approved owners would land the backup on an arbitrary
// machine, so the ambiguity is surfaced instead.
func TestResolveBackupAgentRefusesAmbiguity(t *testing.T) {
	store := storeWith(t,
		controlplane.Worker{ID: "worker-a", TenantID: "tenant-a", StackID: "stack-a", Approved: true},
		controlplane.Worker{ID: "worker-b", TenantID: "tenant-a", StackID: "stack-a", Approved: true},
	)
	o := &Orchestrator{cfg: Config{WorkerStore: store}}

	if _, err := o.resolveBackupAgent(context.Background(), "tenant-a", "stack-a"); err == nil {
		t.Fatal("two approved agents for one stack must refuse rather than guess")
	}
}

func TestResolveBackupAgentRequiresIdentityAndAuthority(t *testing.T) {
	o := &Orchestrator{cfg: Config{WorkerStore: controlplane.NewMemoryStore()}}
	if _, err := o.resolveBackupAgent(context.Background(), "", "stack-a"); err == nil {
		t.Fatal("missing tenant identity must fail")
	}
	bare := &Orchestrator{}
	if _, err := bare.resolveBackupAgent(context.Background(), "tenant-a", "stack-a"); err == nil {
		t.Fatal("a missing worker authority must fail rather than resolve nothing quietly")
	}
}
