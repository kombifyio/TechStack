package orchestrator

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/kombifywallet/walletfake"
)

// With the kombify Wallet as the source, a completed rollout saves its StackKit
// outputs there; the legacy wallet store, which is still wired, is not written.
func TestCompletedRolloutSavesStackKitOutputsInTheKombifyWalletNotTheLegacyStore(t *testing.T) {
	ctx := t.Context()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-wallet", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Wallet", Status: "provisioning",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	fake := walletfake.New(t)
	fake.AllowCreate("owner-1")
	orch := New(&Config{
		Workers: 1, WorkDir: t.TempDir(), StackStore: store, JobStore: store,
		WalletStore: store, KombifyWallet: fake.Client(),
	}, nil)
	defer orch.Stop()

	job := &jobs.Job{
		ID: "job-wallet", Type: jobs.JobTypeDeploy, TargetID: "stack-wallet", State: jobs.JobStateCompleted,
		Payload: map[string]interface{}{stackTenantIDField: "tenant-1", stackOwnerIDField: "owner-1"},
		Result: map[string]interface{}{
			"stackkit_outputs": map[string]interface{}{
				"identity": map[string]interface{}{
					"login_gateway": map[string]interface{}{"url": "https://login.example.com"},
				},
			},
		},
	}
	orch.updateStackStatusSnapshot(job.Snapshot())

	items := fake.Items("owner-1")
	if len(items) != 1 || items[0].Scope != "project:stack-wallet" {
		t.Fatalf("Wallet items = %+v, want the login gateway output in the stack's project", items)
	}
	if rows, err := store.ListWalletItems(ctx, "tenant-1", ""); err != nil || len(rows) != 0 {
		t.Fatalf("the legacy wallet store was written: %d rows, err=%v", len(rows), err)
	}
}
