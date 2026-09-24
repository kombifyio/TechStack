package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/providercontrol"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	jobruntime "github.com/kombifyio/techstack/pkg/jobs"
	"github.com/stretchr/testify/assert"
)

type failingServerRuntimeStore struct {
	controlplane.ServerRuntimeStore
	err error
}

type crudManagedLeaseManager struct {
	preflightErr error
	admitErr     error
	requests     []jobruntime.ManagedLeaseRequest
}

func (m *crudManagedLeaseManager) PreflightCreateOrBindLease(_ context.Context, req jobruntime.ManagedLeaseRequest) error {
	m.requests = append(m.requests, req)
	return m.preflightErr
}

func (m *crudManagedLeaseManager) CreateOrBindLease(_ context.Context, req jobruntime.ManagedLeaseRequest) (*jobruntime.ManagedLeaseResult, error) {
	m.requests = append(m.requests, req)
	if m.admitErr != nil {
		return nil, m.admitErr
	}
	return &jobruntime.ManagedLeaseResult{
		RuntimeSlotKey: jobruntime.PrimaryManagedRuntimeSlotKey, RuntimeSlotGeneration: 1,
		RuntimeSlotID: "slot-native-primary", LeaseID: "lease-native-primary",
		RuntimeServerID: "server-native-primary", ResourceGenerationID: "generation-native-primary",
		OperationID: "operation-native-primary", Provider: req.Provider,
		IdempotentReplay: len(m.requests) > 2,
	}, nil
}

func (s failingServerRuntimeStore) UpsertServerRuntime(context.Context, controlplane.ServerRuntime) (*controlplane.ServerRuntime, error) {
	return nil, s.err
}

func TestListStacksFailsClosedWithoutCanonicalStore(t *testing.T) {
	event, recorder := stackStoreRequestEvent("owner-1", "owner-1")
	_ = (crudRouteHandlers{}).listStacks(event)
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestListStacksUsesControlPlaneStoreWithOwnerTenantFallback(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 5, 28, 15, 0, 0, 0, time.UTC)
	store.SetNow(func() time.Time { return now })

	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:                 "stack-visible",
		TenantID:           "auth0|user-1",
		OwnerSubjectID:     "auth0|user-1",
		HomelabID:          "homelab-visible",
		StackKitInstanceID: "visible-kit",
		Name:               "Visible",
		Mode:               "easy",
		Status:             "running",
		Config: map[string]any{
			"runtime_lane":               "monthly-runtime",
			"server_remote_use_sudo":     true,
			"stackkit_catalog_ref":       "basement-kit",
			"server_connection_mode":     "ssh",
			"server_remote_host_present": true,
			stackConfigKeySpecV2: map[string]any{
				"metadata": map[string]any{"e2e_scenario": "managed-centron"},
			},
		},
	}); err != nil {
		t.Fatalf("CreateStack visible: %v", err)
	}
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-other-owner",
		TenantID:       "auth0|user-1",
		OwnerSubjectID: "auth0|user-2",
		Name:           "Hidden Owner",
	}); err != nil {
		t.Fatalf("CreateStack other owner: %v", err)
	}
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-other-tenant",
		TenantID:       "tenant-2",
		OwnerSubjectID: "auth0|user-1",
		Name:           "Hidden Tenant",
	}); err != nil {
		t.Fatalf("CreateStack other tenant: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stacks", nil)
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{
		UserID: "auth0|user-1",
	}))
	rec := httptest.NewRecorder()
	e := &httpx.Event{
		Request:  req,
		Response: rec,
	}

	if err := (crudRouteHandlers{stackStore: store}).listStacks(e); err != nil {
		t.Fatalf("listStacks: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var envelope ksapi.SuccessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	data, ok := envelope.Data.([]any)
	if !ok {
		t.Fatalf("response data type = %T, want []any", envelope.Data)
	}
	if len(data) != 1 {
		t.Fatalf("response items = %d, want 1: %#v", len(data), data)
	}
	item, ok := data[0].(map[string]any)
	if !ok {
		t.Fatalf("item type = %T, want map[string]any", data[0])
	}
	if item["id"] != "stack-visible" {
		t.Fatalf("item id = %v, want stack-visible", item["id"])
	}
	if item["kit_deployment_id"] != "stack-visible" || item["homelab_id"] != "homelab-visible" || item["stackkit_id"] != "basement-kit" {
		t.Fatalf("canonical product identities = %#v", item)
	}
	if item["stackkit_instance_id"] != "visible-kit" {
		t.Fatalf("explicit identities = %#v", item)
	}
	if item["runtime_lane"] != "monthly-runtime" {
		t.Fatalf("runtime_lane = %v, want monthly-runtime", item["runtime_lane"])
	}
	if item["server_remote_use_sudo"] != true {
		t.Fatalf("server_remote_use_sudo = %v, want true", item["server_remote_use_sudo"])
	}
	if item["e2e_scenario"] != "managed-centron" {
		t.Fatalf("e2e_scenario = %v, want managed-centron", item["e2e_scenario"])
	}
}

func TestGetStackFailsClosedWithoutCanonicalStore(t *testing.T) {
	event, recorder := stackStoreRequestEvent("owner-1", "owner-1")
	_ = (crudRouteHandlers{}).getStack(event)
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestGetStackCanonicalNotFoundDoesNotFallback(t *testing.T) {
	event, _ := stackStoreRequestEvent("owner-1", "owner-1")
	event.Request.SetPathValue("id", "missing-stack")
	err := (crudRouteHandlers{stackStore: controlplane.NewMemoryStore()}).getStack(event)
	assert.Equal(t, http.StatusNotFound, err.(*httpx.APIError).Status)
}

func TestGetStackUsesControlPlaneStoreAndProjectsManagedRuntimeMetadata(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 6, 27, 15, 16, 0, 0, time.UTC)
	store.SetNow(func() time.Time { return now })

	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "mm6p501whcdnh9t",
		TenantID:       "auth0|6a3fe347488118cbd488993c",
		OwnerSubjectID: "auth0|6a3fe347488118cbd488993c",
		Name:           "ril-pro-fixture-ionos-managed",
		Mode:           "easy",
		Status:         "deployed",
		Config: map[string]any{
			"runtime_lane":         "monthly-runtime",
			"stackkit_catalog_ref": "basement-kit",
		},
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	if _, err := store.UpdateStackRuntime(ctx, "auth0|6a3fe347488118cbd488993c", "mm6p501whcdnh9t", controlplane.RuntimeUpdate{
		Status: "deployed",
		RuntimeSummary: map[string]any{
			"runtime_phase":          "verified",
			"lease_provider":         "ionos-managed",
			"provider_region":        "us/ewr",
			"ionos_datacenter":       "us/ewr",
			"lease_id":               "lease-mm6p501whcdnh9t",
			"simulate_provider_id":   "ionos-managed",
			"verification_status":    "verified",
			"stackkit_catalog_ref":   "basement-kit",
			"server_connection_mode": "managed-subscription",
		},
	}); err != nil {
		t.Fatalf("UpdateStackRuntime: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stacks/mm6p501whcdnh9t", nil)
	req.SetPathValue("id", "mm6p501whcdnh9t")
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{
		UserID: "auth0|6a3fe347488118cbd488993c",
	}))
	rec := httptest.NewRecorder()
	e := &httpx.Event{Request: req, Response: rec}

	if err := (crudRouteHandlers{stackStore: store}).getStack(e); err != nil {
		t.Fatalf("getStack: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var envelope ksapi.SuccessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	item, ok := envelope.Data.(map[string]any)
	if !ok {
		t.Fatalf("response data type = %T, want map[string]any", envelope.Data)
	}
	for key, want := range map[string]any{
		"id":                     "mm6p501whcdnh9t",
		"lease_provider":         "ionos-managed",
		"provider_region":        "us/ewr",
		"ionos_datacenter":       "us/ewr",
		"lease_id":               "lease-mm6p501whcdnh9t",
		"simulate_provider_id":   "ionos-managed",
		"runtime_phase":          "verified",
		"verification_status":    "verified",
		"stackkit_catalog_ref":   "basement-kit",
		"server_connection_mode": "managed-subscription",
	} {
		if item[key] != want {
			t.Fatalf("item[%q] = %v, want %v", key, item[key], want)
		}
	}
}

func TestDestroyStackQueuesCanonicalJobForOwnerTenant(t *testing.T) {
	ctx := t.Context()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-1", TenantID: "auth0|user-1", OwnerSubjectID: "auth0|user-1", Name: "Owned",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}

	e, rec := stackStoreRequestEvent("auth0|user-1", "")
	e.Request.SetPathValue("id", "stack-1")

	if err := (crudRouteHandlers{stackStore: store, jobStore: store}).destroyStack(e); err != nil {
		t.Fatalf("destroyStack: %v", err)
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	jobs, err := store.ListJobsByStack(ctx, "auth0|user-1", "stack-1", 10)
	if err != nil {
		t.Fatalf("ListJobsByStack: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Type != "destroy" || jobs[0].State != "pending" {
		t.Fatalf("jobs = %+v, want one pending destroy job", jobs)
	}
}

func TestDestroyStackFailsClosedWithoutCanonicalStores(t *testing.T) {
	e, recorder := stackStoreRequestEvent("owner-1", "owner-1")
	_ = (crudRouteHandlers{}).destroyStack(e)
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestDestroyStackDoesNotFallBackWhenCanonicalStackIsMissing(t *testing.T) {
	e, _ := stackStoreRequestEvent("owner-1", "owner-1")
	e.Request.SetPathValue("id", "missing")
	err := (crudRouteHandlers{stackStore: controlplane.NewMemoryStore(), jobStore: controlplane.NewMemoryStore()}).destroyStack(e)
	assert.Equal(t, http.StatusNotFound, err.(*httpx.APIError).Status)
}

func TestGetStackRejectsOtherOwnerInControlPlaneStore(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-owned",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|owner",
		Name:           "Owned",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stacks/stack-owned", nil)
	req.SetPathValue("id", "stack-owned")
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{
		UserID: "auth0|other",
		OrgID:  "tenant-1",
	}))
	e := &httpx.Event{Request: req, Response: httptest.NewRecorder()}

	if err := (crudRouteHandlers{stackStore: store}).getStack(e); err == nil {
		t.Fatal("getStack returned nil error for another owner's stack")
	}
}

func TestCreateQueuedJobUsesControlPlaneStores(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 5, 28, 16, 0, 0, 0, time.UTC)
	store.SetNow(func() time.Time { return now })
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-1",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "Demo",
		Status:         "pending",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/stacks/stack-1/provision", nil)
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{
		UserID: "auth0|user-1",
		OrgID:  "tenant-1",
	}))
	e := &httpx.Event{
		Request:  req,
		Response: httptest.NewRecorder(),
	}

	jobID, err := (crudRouteHandlers{stackStore: store, jobStore: store}).createQueuedJob(e, queuedJobParams{
		tenantID:    "tenant-1",
		jobType:     "provision",
		stackID:     "stack-1",
		currentStep: "queued",
		stackStatus: "provisioning",
	})
	if err != nil {
		t.Fatalf("createQueuedJob: %v", err)
	}
	if jobID == "" {
		t.Fatal("jobID is empty")
	}
	job, err := store.GetJob(ctx, "tenant-1", jobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.StackID != "stack-1" || job.Type != "provision" || job.State != "pending" {
		t.Fatalf("unexpected job: %#v", job)
	}
	stack, err := store.GetStack(ctx, "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	if stack.Status != "provisioning" {
		t.Fatalf("stack status = %q, want provisioning", stack.Status)
	}
}

func TestPersistStackUsesControlPlaneStore(t *testing.T) {
	store := controlplane.NewMemoryStore()
	event, recorder := stackStoreRequestEvent("auth0|user-1", "")
	h := crudRouteHandlers{stackStore: store, homelabStore: store}

	stack, err := h.persistStack(event, "auth0|user-1", firstNonEmpty(tenantIDFromRequest(event), "auth0|user-1"), normalizedCreateStackRequest{
		Name: "Demo",
		Mode: stackModeEasy,
		UserConfig: map[string]interface{}{
			"runtime_lane":           "monthly-runtime",
			"stackkit_catalog_ref":   "basement-kit",
			"recovery_passphrase":    "must-not-be-stored",
			"recovery_material_ref":  "secret://recovery",
			"server_remote_use_sudo": true,
		},
	})
	if err != nil {
		t.Fatalf("persistStack: %v", err)
	}
	if stack == nil || stack.Id == "" {
		t.Fatalf("persistStack returned %#v", stack)
	}
	if recorder.Code != 0 && recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want no response error", recorder.Code)
	}

	got, err := store.GetStack(context.Background(), "auth0|user-1", stack.Id)
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	if got.OwnerSubjectID != "auth0|user-1" || got.Name != "Demo" || got.Status != "pending" {
		t.Fatalf("unexpected stack: %#v", got)
	}
	if got.Config["runtime_lane"] != "monthly-runtime" || got.Config["server_remote_use_sudo"] != true {
		t.Fatalf("runtime fields not projected into config: %#v", got.Config)
	}
	userConfig, ok := got.Config["user_config"].(map[string]any)
	if !ok {
		t.Fatalf("user_config = %T, want map", got.Config["user_config"])
	}
	if _, ok := userConfig["recovery_passphrase"]; ok {
		t.Fatalf("plaintext recovery material stored in config: %#v", userConfig)
	}
}

func TestPersistStackFailsClosedWithoutCanonicalStores(t *testing.T) {
	event, recorder := stackStoreRequestEvent("owner-1", "owner-1")
	_, _ = (crudRouteHandlers{}).persistStack(event, "owner-1", firstNonEmpty(tenantIDFromRequest(event), "owner-1"), normalizedCreateStackRequest{Name: "Demo"})
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestCreateNormalizedStackRequiresManagedIdempotencyBeforePersist(t *testing.T) {
	store := controlplane.NewMemoryStore()
	event, recorder := stackStoreRequestEvent("auth0|user-1", "tenant-1")
	h := crudRouteHandlers{stackStore: store, homelabStore: store, jobStore: store, managedLeases: &crudManagedLeaseManager{}}
	req := normalizedCreateStackRequest{
		Name: "Managed Without Key", Mode: stackModeEasy,
		UserConfig: map[string]interface{}{
			"server_mode": "monthly-runtime", "runtime_lane": "monthly-runtime",
			"provider_id": "centron", "server_provisioning_mode": "kombify-cloud",
		},
	}
	if err := h.createNormalizedStack(event, "auth0|user-1", firstNonEmpty(tenantIDFromRequest(event), "auth0|user-1"), req); err != nil {
		t.Fatalf("createNormalizedStack: %v", err)
	}
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%s, want 422", recorder.Code, recorder.Body.String())
	}
	stacks, _ := store.ListStacksByTenant(t.Context(), "tenant-1")
	if len(stacks) != 0 {
		t.Fatalf("stacks = %#v, want no persistence without retry identity", stacks)
	}
}

func TestCreateNormalizedStackUsesControlPlaneStores(t *testing.T) {
	store := controlplane.NewMemoryStore()
	event, recorder := stackStoreRequestEvent("auth0|user-1", "tenant-1")
	event.Request.Header.Set("X-Idempotency-Key", "managed-control-plane-store")
	h := crudRouteHandlers{stackStore: store, homelabStore: store, jobStore: store, walletStore: store, managedLeases: &crudManagedLeaseManager{}}

	if err := h.createNormalizedStack(event, "auth0|user-1", firstNonEmpty(tenantIDFromRequest(event), "auth0|user-1"), normalizedCreateStackRequest{
		Name: "Demo",
		Mode: stackModeEasy,
		UserConfig: map[string]interface{}{
			"runtime_lane": "monthly-runtime", "provider_id": "centron",
			ownerConfigKey: map[string]interface{}{
				"bootstrapMode": "custom",
				"source":        "local",
				"email":         "owner@example.test",
			},
			"identity": map[string]interface{}{
				"recovery": map[string]interface{}{
					"passphrase_hash": "$argon2id$v=19$m=65536,t=3,p=4$hash",
				},
			},
		},
	}); err != nil {
		t.Fatalf("createNormalizedStack: %v", err)
	}
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	var response struct {
		Data struct {
			KitDeploymentID string `json:"kit_deployment_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Data.KitDeploymentID == "" {
		t.Fatalf("create response = %s, want canonical kit deployment identity", recorder.Body.String())
	}
	stacks, err := store.ListStacksByTenant(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("ListStacksByTenant: %v", err)
	}
	if len(stacks) != 1 {
		t.Fatalf("stacks = %d, want 1", len(stacks))
	}
	jobs, err := store.ListJobsByStack(context.Background(), "tenant-1", stacks[0].ID, 10)
	if err != nil {
		t.Fatalf("ListJobsByStack: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Type != "provision" {
		t.Fatalf("jobs = %#v, want one provision job", jobs)
	}
	walletItems, err := store.ListWalletItems(context.Background(), "tenant-1", stacks[0].ID)
	if err != nil {
		t.Fatalf("ListWalletItems: %v", err)
	}
	if len(walletItems) != 2 {
		t.Fatalf("wallet items = %#v, want owner and recovery entries", walletItems)
	}
}

func TestCreateNormalizedStackDoesNotDispatchWithoutCanonicalOwnerWallet(t *testing.T) {
	store := controlplane.NewMemoryStore()
	event, recorder := stackStoreRequestEvent("owner-1", "")
	h := crudRouteHandlers{stackStore: store, homelabStore: store, jobStore: store}

	_ = h.createNormalizedStack(event, "owner-1", firstNonEmpty(tenantIDFromRequest(event), "owner-1"), normalizedCreateStackRequest{
		Name: "Owner bootstrap", Mode: stackModeEasy,
		UserConfig: map[string]interface{}{ownerConfigKey: map[string]interface{}{
			"bootstrapMode": ownerBootstrapModeCustom, "source": ownerSourceLocal, "email": "owner@example.test",
		}},
	})
	stacks, _ := store.ListStacksByTenant(t.Context(), "owner-1")
	if recorder.Code != http.StatusInternalServerError || len(stacks) != 1 {
		t.Fatalf("status=%d stacks=%d, want failed bootstrap after canonical stack persistence", recorder.Code, len(stacks))
	}
	jobs, _ := store.ListJobsByStack(t.Context(), "owner-1", stacks[0].ID, 10)
	if len(jobs) != 0 {
		t.Fatalf("status=%d body=%s jobs=%#v, missing owner wallet must stop rollout dispatch", recorder.Code, recorder.Body.String(), jobs)
	}
}

func TestCreateNormalizedStackIdempotentlyReservesManagedServerIdentityAndJob(t *testing.T) {
	store := controlplane.NewMemoryStore()
	manager := &crudManagedLeaseManager{}
	h := crudRouteHandlers{stackStore: store, homelabStore: store, jobStore: store, serverStore: store, managedLeases: manager}
	request := func() normalizedCreateStackRequest {
		return normalizedCreateStackRequest{
			Name: "Managed Demo", Mode: stackModeEasy,
			UserConfig: map[string]interface{}{
				"server_mode": "monthly-runtime", "runtime_lane": "monthly-runtime",
				"provider_id": "centron", "server_provisioning_mode": "kombify-cloud",
			},
		}
	}

	first, firstRecorder := stackStoreRequestEvent("auth0|user-1", "tenant-1")
	first.Request.Header.Set("X-Idempotency-Key", "wizard-attempt-1")
	if err := h.createNormalizedStack(first, "auth0|user-1", firstNonEmpty(tenantIDFromRequest(first), "auth0|user-1"), request()); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if firstRecorder.Code != http.StatusAccepted {
		t.Fatalf("first status = %d body=%s", firstRecorder.Code, firstRecorder.Body.String())
	}

	second, secondRecorder := stackStoreRequestEvent("auth0|user-1", "tenant-1")
	second.Request.Header.Set("X-Idempotency-Key", "wizard-attempt-1")
	if err := h.createNormalizedStack(second, "auth0|user-1", firstNonEmpty(tenantIDFromRequest(second), "auth0|user-1"), request()); err != nil {
		t.Fatalf("second create: %v", err)
	}
	if secondRecorder.Code != http.StatusAccepted {
		t.Fatalf("second status = %d body=%s", secondRecorder.Code, secondRecorder.Body.String())
	}
	var replay map[string]any
	if err := json.Unmarshal(secondRecorder.Body.Bytes(), &replay); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	data, _ := replay["data"].(map[string]any)
	if data["idempotent_replay"] != true || data["kit_deployment_id"] == "" || data["operations_url"] == "" || data["server_id"] == "" {
		t.Fatalf("replay response = %#v", replay)
	}

	stacks, _ := store.ListStacksByTenant(t.Context(), "tenant-1")
	if len(stacks) != 1 {
		t.Fatalf("stacks = %d, want 1", len(stacks))
	}
	jobs, _ := store.ListJobsByStack(t.Context(), "tenant-1", stacks[0].ID, 10)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d, want 1", len(jobs))
	}
	serverID := "server-native-primary"
	servers, _ := store.ListServerRuntimesByTenant(t.Context(), "tenant-1", stacks[0].ID)
	if len(servers) != 0 {
		t.Fatalf("servers = %#v, want native admission to create the closed lease/server aggregate", servers)
	}
	if data["server_id"] != serverID {
		t.Fatalf("replay server_id = %v, want admitted identity %s", data["server_id"], serverID)
	}
	transitions, _ := store.ListServerTransitions(t.Context(), "tenant-1", serverID, 10)
	if len(transitions) != 0 {
		t.Fatalf("server transitions = %d, want native admission to emit the first canonical transitions", len(transitions))
	}
	if len(manager.requests) != 3 {
		t.Fatalf("native admission calls = %d, want preflight+admit then replay admit", len(manager.requests))
	}
	for _, request := range manager.requests {
		if request.StackID != stacks[0].ID || request.OperationKey != jobruntime.PrimaryManagedLeaseOperationKey ||
			request.RuntimeSlotKey != jobruntime.PrimaryManagedRuntimeSlotKey || request.RuntimeSlotGeneration != 1 {
			t.Fatalf("native request = %#v, want exact stack/primary operation identity", request)
		}
	}
}

func TestCreateNormalizedStackStopsBeforeRunnableStateWhenNativePreflightDenies(t *testing.T) {
	store := controlplane.NewMemoryStore()
	manager := &crudManagedLeaseManager{preflightErr: providercontrol.ProviderCreateBlockedError{
		ProviderID: "ionos", ReasonCode: providercontrol.ProviderCreateReasonKillSwitchDisabled,
	}}
	h := crudRouteHandlers{stackStore: store, homelabStore: store, jobStore: store, serverStore: store, managedLeases: manager}
	req := normalizedCreateStackRequest{
		Name: "Blocked Managed Demo", Mode: stackModeEasy,
		UserConfig: map[string]interface{}{
			"server_mode": "monthly-runtime", "runtime_lane": "monthly-runtime",
			"provider_id": "ionos", "server_provisioning_mode": "kombify-cloud",
		},
	}
	event, recorder := stackStoreRequestEvent("auth0|user-1", "tenant-1")
	event.Request.Header.Set("X-Idempotency-Key", "blocked-managed-attempt")
	if err := h.createNormalizedStack(event, "auth0|user-1", firstNonEmpty(tenantIDFromRequest(event), "auth0|user-1"), req); err != nil {
		t.Fatalf("createNormalizedStack: %v", err)
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s, want 503", recorder.Code, recorder.Body.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	apiError, _ := envelope["error"].(map[string]any)
	details, _ := apiError["details"].(map[string]any)
	if details["reason_code"] != providercontrol.ProviderCreateReasonKillSwitchDisabled || details["admission_phase"] != "preflight" {
		t.Fatalf("details = %#v, want stable preflight kill-switch denial", details)
	}
	stacks, _ := store.ListStacksByTenant(t.Context(), "tenant-1")
	if len(stacks) != 1 || stacks[0].Status != "failed" {
		t.Fatalf("stacks = %#v, want one explicit failed audit row", stacks)
	}
	queued, _ := store.ListJobsByStack(t.Context(), "tenant-1", stacks[0].ID, 10)
	servers, _ := store.ListServerRuntimesByTenant(t.Context(), "tenant-1", stacks[0].ID)
	if len(queued) != 0 || len(servers) != 0 || len(manager.requests) != 1 {
		t.Fatalf("jobs=%d servers=%d native_calls=%d, want no runnable state and one read-only preflight", len(queued), len(servers), len(manager.requests))
	}
}

func TestCreateNormalizedStackStopsBeforeDispatchWhenServerIntentFails(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := crudRouteHandlers{
		stackStore:   store,
		homelabStore: store,
		jobStore:     store,
		serverStore: failingServerRuntimeStore{
			ServerRuntimeStore: store,
			err:                errors.New("tenant foreign key missing"),
		},
	}
	// The self-hosted/pairing lane is the one that still publishes a server
	// row during create; the managed lane defers the whole lease/server
	// aggregate to native provider admission and never calls the failing
	// upsert (creation_runtime.go). Exercise the lane the contract covers.
	req := normalizedCreateStackRequest{
		Name: "Self-Host Failure", Mode: stackModeEasy,
		UserConfig: map[string]interface{}{
			"server_provisioning_mode": "connect-remote",
			"server_connection_mode":   "connect-remote",
		},
	}
	event, recorder := stackStoreRequestEvent("auth0|user-1", "tenant-1")
	if err := h.createNormalizedStack(event, "auth0|user-1", firstNonEmpty(tenantIDFromRequest(event), "auth0|user-1"), req); err != nil {
		t.Fatalf("createNormalizedStack: %v", err)
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", recorder.Code, recorder.Body.String())
	}
	stacks, err := store.ListStacksByTenant(t.Context(), "tenant-1")
	if err != nil || len(stacks) != 1 {
		t.Fatalf("stacks = %#v, err=%v", stacks, err)
	}
	jobs, err := store.ListJobsByStack(t.Context(), "tenant-1", stacks[0].ID, 10)
	if err != nil {
		t.Fatalf("ListJobsByStack: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("jobs = %#v, want no dispatch after server intent failure", jobs)
	}
	if stacks[0].Status != "failed" {
		t.Fatalf("stack status = %q, want failed", stacks[0].Status)
	}
}

func TestCreateStackIdempotencyKeyRejectsDifferentPayload(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := crudRouteHandlers{stackStore: store, homelabStore: store}
	first, _ := stackStoreRequestEvent("auth0|user-1", "tenant-1")
	first.Request.Header.Set("X-Idempotency-Key", "wizard-attempt-1")
	if _, err := h.persistStack(first, "auth0|user-1", firstNonEmpty(tenantIDFromRequest(first), "auth0|user-1"), normalizedCreateStackRequest{Name: "One", Mode: stackModeEasy}); err != nil {
		t.Fatalf("first persist: %v", err)
	}

	second, recorder := stackStoreRequestEvent("auth0|user-1", "tenant-1")
	second.Request.Header.Set("X-Idempotency-Key", "wizard-attempt-1")
	stack, err := h.persistStack(second, "auth0|user-1", firstNonEmpty(tenantIDFromRequest(second), "auth0|user-1"), normalizedCreateStackRequest{Name: "Two", Mode: stackModeEasy})
	if err != nil {
		t.Fatalf("mismatch response: %v", err)
	}
	if stack != nil || recorder.Code != http.StatusConflict {
		t.Fatalf("stack=%#v status=%d body=%s", stack, recorder.Code, recorder.Body.String())
	}
}

func TestPersistStackRejectsDuplicateStackKitIdentityWithoutRenaming(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := crudRouteHandlers{stackStore: store, homelabStore: store}
	spec := map[string]any{"metadata": map[string]any{"stackId": "owner-kit"}}
	first, _ := stackStoreRequestEvent("auth0|user-1", "tenant-1")
	if _, err := h.persistStack(first, "auth0|user-1", firstNonEmpty(tenantIDFromRequest(first), "auth0|user-1"), normalizedCreateStackRequest{Name: "One", Mode: stackModeEasy, StackSpecV2: spec}); err != nil {
		t.Fatal(err)
	}
	second, recorder := stackStoreRequestEvent("auth0|user-1", "tenant-1")
	stack, err := h.persistStack(second, "auth0|user-1", firstNonEmpty(tenantIDFromRequest(second), "auth0|user-1"), normalizedCreateStackRequest{Name: "Two", Mode: stackModeEasy, StackSpecV2: spec})
	if err != nil {
		t.Fatal(err)
	}
	if stack != nil || recorder.Code != http.StatusConflict {
		t.Fatalf("stack=%#v status=%d body=%s", stack, recorder.Code, recorder.Body.String())
	}
}

// TestPersistStackStoreDuplicateAutoRenames asserts the product rule
// (2026-05-28): a duplicate stack name must NEVER error — it is auto-resolved
// to a unique name ("Demo" -> "Demo-2"), via the control-plane store path.
func TestPersistStackStoreDuplicateAutoRenames(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := crudRouteHandlers{stackStore: store, homelabStore: store}

	first, _ := stackStoreRequestEvent("auth0|user-1", "tenant-1")
	firstStack, err := h.persistStack(first, "auth0|user-1", firstNonEmpty(tenantIDFromRequest(first), "auth0|user-1"), normalizedCreateStackRequest{Name: "Demo", Mode: stackModeEasy})
	if err != nil {
		t.Fatalf("first persistStack: %v", err)
	}
	if firstStack.Name != "Demo" {
		t.Fatalf("first stack name = %q, want Demo", firstStack.Name)
	}

	second, recorder := stackStoreRequestEvent("auth0|user-1", "tenant-1")
	stack, err := h.persistStack(second, "auth0|user-1", firstNonEmpty(tenantIDFromRequest(second), "auth0|user-1"), normalizedCreateStackRequest{Name: "Demo", Mode: stackModeEasy})
	if err != nil {
		t.Fatalf("duplicate persistStack returned error (must never happen): %v", err)
	}
	if stack == nil {
		t.Fatal("duplicate persistStack returned nil stack; want an auto-renamed stack")
	}
	if recorder.Code != 0 && recorder.Code != http.StatusOK {
		t.Fatalf("duplicate produced HTTP error status %d; body=%s", recorder.Code, recorder.Body.String())
	}
	if stack.Name == "Demo" {
		t.Fatalf("duplicate name was not auto-resolved: still %q", stack.Name)
	}
	if stack.Name != "Demo-2" {
		t.Fatalf("auto-resolved name = %q, want Demo-2", stack.Name)
	}
	if stack.Id == firstStack.Id {
		t.Fatal("duplicate reused the first stack id; want a distinct new stack")
	}
}

func stackStoreRequestEvent(userID, orgID string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/stacks", nil)
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{
		UserID: userID,
		OrgID:  orgID,
	}))
	rec := httptest.NewRecorder()
	return &httpx.Event{
		Request:  req,
		Response: rec,
	}, rec
}
