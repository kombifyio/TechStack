package stacks

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

func stackLifecycleLockEvent(t *testing.T, operation string) (*httpx.Event, *httptest.ResponseRecorder) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"agent_id": "worker-1", "operation": operation,
		"owner_approved": true, "stackkit": "family-lab",
	})
	if err != nil {
		t.Fatal(err)
	}
	event, recorder := stackStoreRequestEvent("auth0|user-1", "tenant-1")
	event.Request.Body = io.NopCloser(bytes.NewReader(payload))
	event.Request.SetPathValue("id", "stack-lock")
	return event, recorder
}

// TestStackLifecycleRefusesToOverrideAServiceLock is the cross-scope half of the
// guardrail: a stack-scoped apply writes to the same services, so silently
// running it past an owner lock would make the lock meaningless. The refusal
// names the services so the owner knows what to unlock, and read-only
// operations stay available.
func TestStackLifecycleRefusesToOverrideAServiceLock(t *testing.T) {
	store := controlplane.NewMemoryStore()
	seedStack(t, store, "stack-lock", "tenant-1", "auth0|user-1", "active")
	seedApprovedWorker(t, store, "worker-1", "tenant-1", "auth0|user-1", "stack-lock", "192.0.2.10")
	if _, err := store.UpsertServiceRuntime(t.Context(), controlplane.ServiceRuntime{
		ID: "service-locked", TenantID: "tenant-1", StackID: "stack-lock", ServerID: "server-1",
		ServiceKey: "auth",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetServiceMutationLock(t.Context(), "tenant-1", "service-locked",
		controlplane.ServiceMutationLock{
			State: serviceregistry.MutationLocked, ReasonCode: "owner_locked", Actor: "auth0|user-1",
		}, "owner_locked"); err != nil {
		t.Fatal(err)
	}
	orch := orchestrator.New(&orchestrator.Config{
		Workers: 1, StackStore: store, JobStore: store, WorkerStore: store,
		LeaseLister: routingTestLeaseLister{},
	}, nil)
	defer orch.Stop()
	h := crudRouteHandlers{orch: orch, stackStore: store, jobStore: store, serviceStore: store}

	event, recorder := stackLifecycleLockEvent(t, "apply")
	if err := h.startStackKitLifecycle(event); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusConflict {
		t.Fatalf("apply over a locked service status = %d, want 409", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "service-locked") {
		t.Fatalf("refusal did not name the locked service: %s", recorder.Body.String())
	}

	// Inspection is not a mutation: an owner has to be able to look at a stack
	// that carries a lock.
	readOnly, readRecorder := stackLifecycleLockEvent(t, "plan")
	if err := h.startStackKitLifecycle(readOnly); err != nil {
		t.Fatal(err)
	}
	if readRecorder.Code == http.StatusConflict &&
		strings.Contains(readRecorder.Body.String(), "locked_service_ids") {
		t.Fatalf("plan was refused by the service lock: %s", readRecorder.Body.String())
	}
}
