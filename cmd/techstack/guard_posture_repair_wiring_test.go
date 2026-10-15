package main

import (
	"context"
	"testing"

	agentpb "github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/guardposture"
	"github.com/kombifyio/techstack/pkg/orchestrator"
)

type idleStackKitSender struct{}

func (idleStackKitSender) SendStackKitCommand(context.Context, string, *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	return nil, nil
}

// Regression (Q-4): the posture service is composed before route wiring
// installs the StackKit commander, and repair stayed unavailable for the life of
// the process. A repair request must see the commander installed after boot.
func TestGuardPostureRepairFollowsCommanderInstalledAfterBoot(t *testing.T) {
	orch := orchestrator.New(&orchestrator.Config{Workers: 0}, nil)
	service, err := guardposture.NewService(guardposture.ServiceConfig{
		Store:      guardposture.NewMemoryStore(nil),
		Dispatcher: guardPostureRepairDispatcher(orch),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	request := guardposture.RepairRequest{
		Identity: guardposture.Identity{TenantID: "tenant-1", WorkerID: "worker-1", ServerID: "server-1"},
		Controls: []string{guardposture.ControlBruteForce},
		Mode:     guardposture.RepairOperator,
	}
	denied := func() string {
		t.Helper()
		_, err := service.RequestRepair(context.Background(), request)
		denial, ok := guardposture.IsRepairDenial(err)
		if !ok {
			t.Fatalf("repair was not denied with a structured reason: %v", err)
		}
		return denial.Code
	}

	if code := denied(); code != guardposture.DenyUnavailable {
		t.Fatalf("before the commander is installed: denial %q, want %q", code, guardposture.DenyUnavailable)
	}
	orch.ConfigureStackKitCommander(idleStackKitSender{})
	if code := denied(); code == guardposture.DenyUnavailable {
		t.Fatalf("repair still unavailable after the commander was installed")
	}
}
