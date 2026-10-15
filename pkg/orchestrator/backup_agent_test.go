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

// A Modern stack has two approved agents. The rollout addresses the Cloud node
// by name, so it must get the agent whose host spells that node and never the
// Home agent, an unapproved enrollment, or another owner's agent.
func TestResolveStackKitNodeAgentReturnsOnlyTheNodesOwnApprovedAgent(t *testing.T) {
	stack := func(id, host, owner string, approved bool) controlplane.Worker {
		return controlplane.Worker{ID: id, TenantID: "tenant-a", StackID: "stack-a", Hostname: host, OwnerSubjectID: owner, Approved: approved}
	}
	store := storeWith(t,
		stack("worker-home", "home-main", "owner-a", true),
		stack("worker-cloud", "cloud-edge", "owner-a", true),
		stack("worker-stranger", "cloud-edge", "owner-b", true),
	)
	o := &Orchestrator{workerStore: store}
	agent, err := o.resolveStackKitNodeAgent(context.Background(), "tenant-a", "owner-a", "stack-a", "cloud-edge")
	if err != nil || agent != "worker-cloud" {
		t.Fatalf("agent = %q err = %v, want the Cloud node's own agent", agent, err)
	}
	if agent, _ := o.resolveStackKitNodeAgent(context.Background(), "tenant-a", "owner-a", "stack-a", "home-standby"); agent != "" {
		t.Fatalf("a node with no agent resolved to %q", agent)
	}
	twin := storeWith(t, stack("worker-1", "cloud-edge", "owner-a", true), stack("worker-2", "cloud-edge", "owner-a", true))
	if _, err := (&Orchestrator{workerStore: twin}).resolveStackKitNodeAgent(context.Background(), "tenant-a", "owner-a", "stack-a", "cloud-edge"); err == nil {
		t.Fatal("two agents for one node must be refused, not guessed")
	}
}
