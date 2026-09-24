package trust

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
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
func TestMintStackPairingTokenConnectRemoteCreatesRemoteEnrollmentJob(t *testing.T) {
	store := controlplane.NewMemoryStore()
	ctx := context.Background()
	stack, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-remote", TenantID: "tenant-mint", OwnerSubjectID: "owner-mint", Name: "Remote Stack",
	})
	if err != nil {
		t.Fatalf("seed stack: %v", err)
	}
	stores := RouteStores{Stacks: store, Workers: store, Jobs: store}
	minted, err := MintStackPairingToken(ctx, stores, "tenant-mint", "owner-mint", stack, PairingTokenParams{
		Name:                   "Remote Node",
		StackID:                stack.ID,
		ServerProvisioningMode: "connect-remote",
		ServerRemoteHost:       "node.example.test",
		ServerRemoteUser:       "root",
	})
	if err != nil {
		t.Fatalf("MintStackPairingToken: %v", err)
	}
	job, err := store.GetJob(ctx, "tenant-mint", minted.JobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.Type != "remote_enrollment" || job.State != "pending" {
		t.Fatalf("unexpected connect-remote job: type=%q state=%q", job.Type, job.State)
	}
}

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

func TestListPairingTokensIsTenantAndOwnerScoped(t *testing.T) {
	store := controlplane.NewMemoryStore()
	ctx := context.Background()
	for _, token := range []controlplane.PairingToken{
		{ID: "visible", TenantID: "tenant-1", OwnerSubjectID: "owner-1", TokenHash: "hash-1", Status: "active"},
		{ID: "other-owner", TenantID: "tenant-1", OwnerSubjectID: "owner-2", TokenHash: "hash-2", Status: "active"},
		{ID: "other-tenant", TenantID: "tenant-2", OwnerSubjectID: "owner-1", TokenHash: "hash-3", Status: "active"},
	} {
		if _, err := store.UpsertPairingToken(ctx, token); err != nil {
			t.Fatalf("seed token: %v", err)
		}
	}

	event, recorder := pairingRouteTestEvent(http.MethodGet, "/api/v1/trust/pairing-tokens", "owner-1", "tenant-1", nil)
	if err := listPairingTokensFromStore(store)(event); err != nil {
		t.Fatalf("list pairing tokens: %v", err)
	}
	var response struct {
		Data struct {
			Tokens []struct {
				ID string `json:"id"`
			} `json:"tokens"`
			Count int `json:"count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Data.Count != 1 || len(response.Data.Tokens) != 1 || response.Data.Tokens[0].ID != "visible" {
		t.Fatalf("list crossed tenant or owner boundary: %#v", response.Data)
	}
}

func TestDeletePairingTokenCannotCrossOwnerBoundary(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertPairingToken(context.Background(), controlplane.PairingToken{
		ID: "token-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", TokenHash: "hash-1", Status: "active",
	}); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	event, recorder := pairingRouteTestEvent(http.MethodDelete, "/api/v1/trust/pairing-tokens/token-1", "owner-2", "tenant-1", nil)
	event.Request.SetPathValue("id", "token-1")
	if err := deletePairingTokenFromStore(store)(event); err != nil {
		t.Fatalf("delete pairing token: %v", err)
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-owner delete status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
	token, err := store.GetPairingTokenByHash(context.Background(), "tenant-1", "hash-1")
	if err != nil || token.Status != "active" {
		t.Fatalf("cross-owner delete changed token: token=%#v err=%v", token, err)
	}
}

func pairingRouteTestEvent(method, target, ownerID, tenantID string, body []byte) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: ownerID, OrgID: tenantID}))
	recorder := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: recorder}, recorder
}
