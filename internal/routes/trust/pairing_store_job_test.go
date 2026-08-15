package trust

import (
	"context"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

// TestCreateStorePairingJobReturnsCreationJob covers the BYOS add-server fix:
// post-PocketBase, the control-plane store path must mint the completed
// registration job the wizard polls, or the BYOS lane 500s on an empty job_id.
func TestCreateStorePairingJobReturnsCreationJob(t *testing.T) {
	store := controlplane.NewMemoryStore()
	ctx := context.Background()
	stack, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-byos",
		TenantID:       "tenant-byos",
		OwnerSubjectID: "owner-byos",
		Name:           "BYOS Stack",
		Status:         "pending",
	})
	if err != nil {
		t.Fatalf("seed stack: %v", err)
	}

	port := 2222
	result := storePairingJobResult(pairingTokenRequest{
		ServerProvisioningMode: "connect-remote",
		NodeRole:               "worker",
		StackKit:               "basement-kit",
		Services:               []string{"vaultwarden"},
		ServerRemoteHost:       "10.0.0.5",
		ServerRemotePort:       &port,
		ServerRemoteUser:       "ubuntu",
		ServerRemoteUseSudo:    true,
	}, "raw-pairing-token", time.Now().UTC().Add(15*time.Minute))
	if result["registration_token"] != "raw-pairing-token" {
		t.Fatalf("job result must carry the registration token, got %#v", result)
	}
	if result["server_remote_host"] != "10.0.0.5" || result["server_remote_use_sudo"] != true {
		t.Fatalf("job result missing remote provisioning hints: %#v", result)
	}

	jobID, err := createStorePairingJob(ctx, store, stack, result)
	if err != nil {
		t.Fatalf("createStorePairingJob: %v", err)
	}
	if jobID == "" {
		t.Fatal("BYOS registration must return a creation job id")
	}
	job, err := store.GetJob(ctx, "tenant-byos", jobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.State != "completed" || job.StackID != "stack-byos" {
		t.Fatalf("unexpected registration job: state=%q stack=%q", job.State, job.StackID)
	}
	if job.Result["registration_token"] != "raw-pairing-token" {
		t.Fatalf("registration job dropped the token: %#v", job.Result)
	}
}

// TestCreateStorePairingJobToleratesNilJobStore keeps token issuance working
// even when the job store is not wired (partial config) — no job, no error.
func TestCreateStorePairingJobToleratesNilJobStore(t *testing.T) {
	jobID, err := createStorePairingJob(context.Background(), nil, &controlplane.Stack{ID: "s"}, map[string]any{})
	if err != nil || jobID != "" {
		t.Fatalf("nil job store must yield empty id and no error, got id=%q err=%v", jobID, err)
	}
}

// TestMintStackPairingTokenMintsTokenAndJob covers the exported mint core the
// wizard-run facade composes: token persisted with node-handoff metadata plus
// the completed registration job carrying the raw token.
func TestMintStackPairingTokenMintsTokenAndJob(t *testing.T) {
	store := controlplane.NewMemoryStore()
	ctx := context.Background()
	stack, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-mint",
		TenantID:       "tenant-mint",
		OwnerSubjectID: "owner-mint",
		Name:           "Mint Stack",
	})
	if err != nil {
		t.Fatalf("seed stack: %v", err)
	}
	stores := RouteStores{Stacks: store, Workers: store, Jobs: store}

	minted, err := MintStackPairingToken(ctx, stores, "tenant-mint", "owner-mint", stack, PairingTokenParams{
		Name:                   "Mint Stack main",
		StackID:                stack.ID,
		ServerProvisioningMode: "install-command",
		NodeRole:               "worker",
		StackKit:               "basement-kit",
	})
	if err != nil {
		t.Fatalf("MintStackPairingToken: %v", err)
	}
	if minted.Token == "" || minted.TokenID == "" || minted.JobID == "" {
		t.Fatalf("mint incomplete: %#v", minted)
	}
	if minted.ExpiresAt.Before(time.Now().UTC().Add(10 * time.Minute)) {
		t.Fatalf("default TTL must be ~15 minutes, got %v", minted.ExpiresAt)
	}

	job, err := store.GetJob(ctx, "tenant-mint", minted.JobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.Result["registration_token"] != minted.Token {
		t.Fatalf("registration job must carry the raw token: %#v", job.Result)
	}
	if job.Result["stackkit_foundation"] != "basement-kit" || job.Result["server_node_role"] != "worker" {
		t.Fatalf("registration job missing handoff hints: %#v", job.Result)
	}
}

// TestMintStackPairingTokenStackLessSkipsJob keeps the stack-less mint shape
// of the trust endpoint intact: token only, no registration job.
func TestMintStackPairingTokenStackLessSkipsJob(t *testing.T) {
	store := controlplane.NewMemoryStore()
	stores := RouteStores{Workers: store, Jobs: store}

	minted, err := MintStackPairingToken(context.Background(), stores, "tenant-mint", "owner-mint", nil, PairingTokenParams{Name: "loose token"})
	if err != nil {
		t.Fatalf("MintStackPairingToken: %v", err)
	}
	if minted.Token == "" || minted.JobID != "" {
		t.Fatalf("stack-less mint must yield a token and no job: %#v", minted)
	}
}
