package routes

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/agentcontrol"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/guardbinding"
)

type fakeOutcomeControl struct {
	resolved []agentcontrol.OperatorResolution
}

func (*fakeOutcomeControl) Poll(context.Context, string, []string) (*agentpb.StackKitCommand, bool, error) {
	return nil, false, nil
}
func (*fakeOutcomeControl) SubmitResult(string, *agentpb.StackKitResult) error { return nil }
func (*fakeOutcomeControl) ListUnresolvedOutcomes(context.Context, string, string) ([]agentcontrol.UnresolvedOutcome, error) {
	return nil, nil
}
func (f *fakeOutcomeControl) ResolveOutcomeByOperator(_ context.Context, r agentcontrol.OperatorResolution) (guardbinding.ResultReport, error) {
	f.resolved = append(f.resolved, r)
	return guardbinding.ResultReport{Revision: r.ExpectedRevision + 1, Resolution: r.Disposition}, nil
}

type stepUpGate struct {
	EntitlementGate
	fresh bool
}

func (g stepUpGate) FreshMultiFactor(context.Context, time.Time, time.Duration) bool { return g.fresh }

// Only the owner of a worker, with a fresh second factor, releases an
// unresolved outcome; the recorded actor is the authenticated subject, never a
// value from the request.
func TestOperatorOutcomeResolutionIsScopedToTheOwnerWithFreshStepUp(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{ID: "worker-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Status: "connected", Approved: true}); err != nil {
		t.Fatal(err)
	}
	control := &fakeOutcomeControl{}
	body := map[string]any{"disposition": "verified_applied", "expected_revision": 2, "note": "checked the unit"}
	call := func(owner string, fresh bool, payload map[string]any) int {
		h := workerRouteHandlers{wst: store, typedControl: control, entitlements: stepUpGate{EntitlementGate: selfHostedEntitlementGate{}, fresh: fresh}}
		event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/workers/worker-1/outcomes/cmd-1/resolve", owner, "tenant-1", payload)
		event.Request.SetPathValue("id", "worker-1")
		event.Request.SetPathValue("command_id", "cmd-1")
		_ = h.resolveOutcome(event)
		return recorder.Code
	}
	if code := call("owner-2", true, body); code != http.StatusNotFound {
		t.Fatalf("another owner got %d", code)
	}
	if code := call("owner-1", false, body); code != http.StatusForbidden {
		t.Fatalf("without a fresh second factor got %d", code)
	}
	if code := call("owner-1", true, map[string]any{"disposition": "verified_applied", "expected_revision": 2, "note": "x", "actor_id": "someone-else"}); code != http.StatusBadRequest {
		t.Fatalf("an unknown field got %d", code)
	}
	if len(control.resolved) != 0 {
		t.Fatalf("a refused request resolved %d outcomes", len(control.resolved))
	}
	if code := call("owner-1", true, body); code != http.StatusOK {
		t.Fatalf("owner with step-up got %d", code)
	}
	if len(control.resolved) != 1 || control.resolved[0].ActorID != "owner-1" || control.resolved[0].TenantID != "tenant-1" {
		t.Fatalf("resolution = %+v", control.resolved)
	}
}
