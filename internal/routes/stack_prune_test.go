package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	jobruntime "github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

var pruneTestNow = time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)

type errListLeaseService struct{}

func (errListLeaseService) Get(context.Context, string, vmlease.LeaseID) (*vmlease.Lease, error) {
	return nil, errors.New("lease get boom")
}

func (errListLeaseService) ListByTenant(context.Context, string) ([]vmlease.Lease, error) {
	return nil, errors.New("lease list boom")
}

func (errListLeaseService) Patch(context.Context, string, vmlease.LeaseID, vmleases.PatchRequest) (*vmlease.Lease, error) {
	return nil, errors.New("lease patch boom")
}

type countingPruneDecommissioner struct{ requests int }

func (d *countingPruneDecommissioner) DecommissionManagedLeases(context.Context, jobruntime.ManagedLeaseDecommissionRequest) (*jobruntime.ManagedLeaseDecommissionResult, error) {
	d.requests++
	return nil, errors.New("projection cleanup called provider lifecycle")
}

func newPruneTestLeaseService() *vmleases.Service {
	return vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{
		Now:            func() time.Time { return pruneTestNow },
		SnapshotSecret: []byte("secret"),
	})
}

func newPruneTestStore() *controlplane.MemoryStore {
	store := controlplane.NewMemoryStore()
	store.SetNow(func() time.Time { return pruneTestNow })
	return store
}

func seedControlPlaneStack(t *testing.T, store *controlplane.MemoryStore, tenantID, ownerID, stackID string) {
	t.Helper()
	seedPruneStack(t, store, tenantID, ownerID, stackID, stackID, nil)
}

func seedPruneStack(t *testing.T, store *controlplane.MemoryStore, tenantID, ownerID, stackID, name string, config map[string]any) {
	t.Helper()
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{
		ID: stackID, TenantID: tenantID, OwnerSubjectID: ownerID, Name: name, Status: "pending", Config: config,
	}); err != nil {
		t.Fatalf("seed control-plane stack: %v", err)
	}
}

func seedPruneWorker(t *testing.T, store *controlplane.MemoryStore, id, ownerID, stackID, hostname string, lastSeen time.Time) {
	t.Helper()
	if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
		ID: id, TenantID: "tenant-1", OwnerSubjectID: ownerID, StackID: stackID, Hostname: hostname, LastSeenAt: &lastSeen,
	}); err != nil {
		t.Fatalf("seed worker: %v", err)
	}
}

func executePruneRequest(t *testing.T, handler stackLifecycleRouteHandlers, body string) (*httptest.ResponseRecorder, stackPruneResponse) {
	t.Helper()
	event, recorder := stackLifecycleRouteTestEvent(http.MethodPost, "/api/v1/stacks/prune-orphans", body, "owner-1", "tenant-1")
	if err := handler.pruneOrphanStacks(event); err != nil {
		t.Fatalf("pruneOrphanStacks returned router error: %v", err)
	}
	var envelope struct {
		Data stackPruneResponse `json:"data"`
	}
	if recorder.Code == http.StatusOK {
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode prune response: %v", err)
		}
	}
	return recorder, envelope.Data
}

// Reproduced regression and sensitive destructive boundary: planning returns
// only explicitly test-owned residue and never changes the inventory.
func TestPruneOrphansDryRunPlansOwnerScopedResidueWithoutMutation(t *testing.T) {
	store := newPruneTestStore()
	seedPruneStack(t, store, "tenant-1", "owner-1", "stack-e2e", "e2e-ionos-20260826090000", nil)
	seedPruneStack(t, store, "tenant-1", "owner-1", "stack-runtime", "runtime-cloud-centron-20260826090100", nil)
	seedPruneStack(t, store, "tenant-1", "owner-1", "stack-persistent", "family-homelab", nil)
	seedPruneStack(t, store, "tenant-1", "owner-1", "stack-protected", "e2e-protected-20260826090200", nil)
	seedPruneStack(t, store, "tenant-1", "owner-1", "stack-invalid-timestamp", "runtime-cloud-ionos-20261399000000", nil)
	seedPruneStack(t, store, "tenant-1", "owner-1", "stack-demo", "e2e-demo-20260826090300", map[string]any{"demo_anchor": true})
	seedPruneStack(t, store, "tenant-1", "owner-2", "stack-foreign", "e2e-foreign-20260826090400", nil)
	seedPruneStack(t, store, "tenant-1", "owner-1", "stack-live", "e2e-live-20260826090500", nil)
	seedPruneStack(t, store, "tenant-1", "owner-1", "stack-recent-worker", "e2e-recent-20260826090600", nil)
	seedPruneWorker(t, store, "worker-stale", "owner-1", "missing-stack", "e2e-stale-worker", pruneTestNow.Add(-25*time.Hour))
	seedPruneWorker(t, store, "worker-recent", "owner-1", "missing-stack", "e2e-recent-worker", pruneTestNow.Add(-time.Hour))
	seedPruneWorker(t, store, "worker-attached", "owner-1", "stack-recent-worker", "e2e-attached-worker", pruneTestNow.Add(-time.Hour))

	leases := newPruneTestLeaseService()
	live := createStackOperationsTestLease("lease-live", "tenant-1", "owner-1", "stack-live", "enrolled")
	live.Resource.EngineVMID = "provider-server-live"
	if _, err := leases.CreateOrUpdate(t.Context(), vmleases.CreateRequest{Lease: live}); err != nil {
		t.Fatalf("seed live lease: %v", err)
	}
	handler := stackLifecycleRouteHandlers{leases: leases, stacks: store, workers: store, now: func() time.Time { return pruneTestNow }}

	firstRecorder, first := executePruneRequest(t, handler, `{"mode":"dry_run"}`)
	if firstRecorder.Code != http.StatusOK || first.Digest == "" {
		t.Fatalf("dry-run status=%d response=%+v", firstRecorder.Code, first)
	}
	candidates := map[string]bool{}
	for _, candidate := range first.Candidates {
		candidates[candidate.ID] = true
	}
	for _, eligible := range []string{"stack-e2e", "stack-runtime", "worker-stale"} {
		if !candidates[eligible] {
			t.Errorf("eligible residue %s missing from plan: %+v", eligible, first.Candidates)
		}
	}
	for _, protected := range []string{"stack-persistent", "stack-protected", "stack-invalid-timestamp", "stack-demo", "stack-foreign", "stack-live", "stack-recent-worker", "worker-recent", "worker-attached"} {
		if candidates[protected] {
			t.Errorf("protected projection %s entered plan: %+v", protected, first.Candidates)
		}
	}
	_, second := executePruneRequest(t, handler, `{"mode":"dry_run"}`)
	if second.Digest != first.Digest {
		t.Fatalf("unchanged dry-run digest drifted: %q then %q", first.Digest, second.Digest)
	}
	if _, err := store.GetStack(t.Context(), "tenant-1", "stack-e2e"); err != nil {
		t.Fatalf("dry-run changed stack inventory: %v", err)
	}
	if _, err := store.GetWorker(t.Context(), "tenant-1", "worker-stale"); err != nil {
		t.Fatalf("dry-run changed worker inventory: %v", err)
	}
}

func TestPruneOrphansApplyFailsClosedWhenPlanDrifts(t *testing.T) {
	store := newPruneTestStore()
	seedPruneStack(t, store, "tenant-1", "owner-1", "stack-e2e", "e2e-ionos-20260826090000", nil)
	handler := stackLifecycleRouteHandlers{leases: newPruneTestLeaseService(), stacks: store, workers: store, now: func() time.Time { return pruneTestNow }}
	_, plan := executePruneRequest(t, handler, `{"mode":"dry_run"}`)
	seedPruneWorker(t, store, "worker-new", "owner-1", "stack-e2e", "e2e-new-worker", pruneTestNow)

	body, _ := json.Marshal(stackPruneRequest{Mode: stackPruneModeApply, Digest: plan.Digest})
	recorder, _ := executePruneRequest(t, handler, string(body))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("drifted apply status=%d body=%s, want conflict", recorder.Code, recorder.Body.String())
	}
	if _, err := store.GetStack(t.Context(), "tenant-1", "stack-e2e"); err != nil {
		t.Fatalf("drifted apply changed stack inventory: %v", err)
	}
}

func TestPruneOrphansAppliesExactStackThenStaleWorkerPlansWithoutProviderLifecycle(t *testing.T) {
	store := newPruneTestStore()
	seedPruneStack(t, store, "tenant-1", "owner-1", "stack-e2e", "e2e-ionos-20260826090000", nil)
	seedPruneWorker(t, store, "worker-stale", "owner-1", "stack-e2e", "e2e-ionos-worker", pruneTestNow.Add(-25*time.Hour))
	decommissioner := &countingPruneDecommissioner{}
	handler := stackLifecycleRouteHandlers{
		leases: newPruneTestLeaseService(), stacks: store, workers: store, decommissioner: decommissioner, now: func() time.Time { return pruneTestNow },
	}

	_, stackPlan := executePruneRequest(t, handler, `{"mode":"dry_run","stack_id":"stack-e2e"}`)
	stackApply, _ := json.Marshal(stackPruneRequest{Mode: stackPruneModeApply, StackID: "stack-e2e", Digest: stackPlan.Digest})
	recorder, appliedStack := executePruneRequest(t, handler, string(stackApply))
	if recorder.Code != http.StatusOK || appliedStack.Applied.Stacks == 0 {
		t.Fatalf("stack apply status=%d response=%+v", recorder.Code, appliedStack)
	}
	if _, err := store.GetStack(t.Context(), "tenant-1", "stack-e2e"); !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatalf("stack projection remains after exact apply: %v", err)
	}

	_, workerPlan := executePruneRequest(t, handler, `{"mode":"dry_run","stack_id":"stack-e2e"}`)
	workerApply, _ := json.Marshal(stackPruneRequest{Mode: stackPruneModeApply, StackID: "stack-e2e", Digest: workerPlan.Digest})
	recorder, appliedWorker := executePruneRequest(t, handler, string(workerApply))
	if recorder.Code != http.StatusOK || appliedWorker.Applied.Workers == 0 {
		t.Fatalf("worker apply status=%d response=%+v", recorder.Code, appliedWorker)
	}
	if _, err := store.GetWorker(t.Context(), "tenant-1", "worker-stale"); !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatalf("worker projection remains after exact apply: %v", err)
	}
	_, emptyPlan := executePruneRequest(t, handler, `{"mode":"dry_run","stack_id":"stack-e2e"}`)
	if len(emptyPlan.Candidates) != 0 {
		t.Fatalf("cleanup residue remains: %+v", emptyPlan.Candidates)
	}
	if decommissioner.requests != 0 {
		t.Fatalf("projection cleanup invoked provider lifecycle %d time(s)", decommissioner.requests)
	}
}

func TestPruneOrphansFailsClosedWhenAttachmentAuthorityCannotBeRead(t *testing.T) {
	store := newPruneTestStore()
	seedPruneStack(t, store, "tenant-1", "owner-1", "stack-e2e", "e2e-ionos-20260826090000", nil)
	handler := stackLifecycleRouteHandlers{leases: errListLeaseService{}, stacks: store, workers: store, now: func() time.Time { return pruneTestNow }}
	recorder, _ := executePruneRequest(t, handler, `{"mode":"dry_run"}`)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("unverified attachment status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, err := store.GetStack(t.Context(), "tenant-1", "stack-e2e"); err != nil {
		t.Fatalf("unverified attachment changed inventory: %v", err)
	}
}
