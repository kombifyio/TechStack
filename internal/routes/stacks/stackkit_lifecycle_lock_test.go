package stacks

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

func stackLifecycleLockEvent(t *testing.T, operation string) (*httpx.Event, *httptest.ResponseRecorder) {
	t.Helper()
	input := map[string]any{
		"agent_id": "worker-1", "operation": operation,
		"owner_approved": true, "stackkit": "family-lab",
	}
	if operation == "backup_restore_activate" || operation == "backup_restore_recover" {
		input["restore_result_id"] = "sha256:" + strings.Repeat("a", 64)
	}
	if operation == "backup_restore_recover" {
		input["activation_operation_id"] = "restore-original-job"
		input["restore_plan_hash"] = "sha256:" + strings.Repeat("b", 64)
	}
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	event, recorder := stackStoreRequestEvent("auth0|user-1", "tenant-1")
	event.Request.Body = io.NopCloser(bytes.NewReader(payload))
	event.Request.SetPathValue("id", "stack-lock")
	if operation == "backup_restore_activate" {
		event.Request.Header.Set("Idempotency-Key", "activation-lock-probe")
	}
	return event, recorder
}

// An accepted request survives a lost response and controller queue without
// creating a new activation, even when mutable runtime eligibility changes.
func TestRestoreActivationHTTPRetryRetainsExactAcceptedIntent(t *testing.T) {
	store := controlplane.NewMemoryStore()
	rollout := jobs.StackKitRolloutBinding{StackKit: "basement-kit", SpecPath: "stack-spec.v2.json"}
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{ID: "restore-stack", TenantID: "tenant-1", OwnerSubjectID: "auth0|user-1", StackKitInstanceID: "home", Name: "Home", Status: "running", Config: map[string]any{jobs.StackKitRolloutBindingResultField: rollout.Map()}}); err != nil {
		t.Fatal(err)
	}
	seedApprovedWorker(t, store, "worker-1", "tenant-1", "auth0|user-1", "restore-stack", "192.0.2.10")
	orch := orchestrator.New(&orchestrator.Config{Workers: 0, StackStore: store, JobStore: store, WorkerStore: store, LeaseLister: routingTestLeaseLister{}}, nil)
	defer orch.Stop()
	h := crudRouteHandlers{orch: orch, stackStore: store, jobStore: store, serviceStore: store, serverStore: store}
	resultID := "sha256:" + strings.Repeat("a", 64)
	targetStack := "restore-stack"
	invoke := func(agent, result string, approved bool) (stackKitLifecycleResponse, *httptest.ResponseRecorder) {
		event, recorder := stackStoreRequestEvent("auth0|user-1", "tenant-1")
		event.Request.SetPathValue("id", targetStack)
		event.Request.Header.Set("Idempotency-Key", "accepted-ack-lost")
		body, _ := json.Marshal(map[string]any{"agent_id": agent, "operation": "backup_restore_activate", "restore_result_id": result, "owner_approved": approved})
		event.Request.Body = io.NopCloser(bytes.NewReader(body))
		if err := h.startStackKitLifecycle(event); err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Data stackKitLifecycleResponse `json:"data"`
		}
		_ = json.Unmarshal(recorder.Body.Bytes(), &envelope)
		return envelope.Data, recorder
	}
	// The first POST was accepted; discard its response as a lost HTTP ACK.
	first, response := invoke("worker-1", resultID, true)
	if response.Code != http.StatusAccepted || first.JobID == "" {
		t.Fatalf("acceptance: %d %s", response.Code, response.Body.String())
	}
	retry, retryResponse := invoke("worker-1", resultID, true)
	if retryResponse.Code != http.StatusAccepted || retry.JobID != first.JobID {
		t.Fatal("lost-ACK retry created another job")
	}
	accepted, err := store.GetJob(t.Context(), "tenant-1", first.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Result[controlplane.RestoreActivationRequestResultKey] == nil || len(accepted.Payload) == 0 {
		t.Fatal("acceptance was not durably correlated before admission")
	}
	// Another controller has lost the local queue. It rehydrates the retained
	// pending payload under the SAME durable identity; the execution claim
	// remains the arbiter, rather than a new request or new activation identity.
	restarted := orchestrator.New(&orchestrator.Config{Workers: 0, StackStore: store, JobStore: store, WorkerStore: store, LeaseLister: routingTestLeaseLister{}}, nil)
	defer restarted.Stop()
	h.orch = restarted
	resumed, resumeResponse := invoke("worker-1", resultID, true)
	if resumeResponse.Code != http.StatusAccepted || resumed.JobID != first.JobID {
		t.Fatal("lost queue produced a new activation request")
	}
	if queued, found := restarted.Queue().Get(first.JobID); !found || queued.Payload["restore_result_id"] != resultID {
		t.Fatal("lost queue did not preserve accepted payload")
	}
	if _, err = store.StartJob(t.Context(), "tenant-1", first.JobID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CompleteJob(t.Context(), "tenant-1", first.JobID, accepted.Result, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	worker, err := store.GetWorker(t.Context(), "tenant-1", "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	worker.Approved = false
	if _, err = store.UpsertWorkerHeartbeat(t.Context(), *worker); err != nil {
		t.Fatal(err)
	}
	after, afterResponse := invoke("worker-1", resultID, true)
	if afterResponse.Code != http.StatusAccepted || after.JobID != first.JobID {
		t.Fatalf("terminal replay after node change: %d %s", afterResponse.Code, afterResponse.Body.String())
	}
	for _, change := range []struct {
		agent, result string
		approved      bool
	}{{"other-node", resultID, true}, {"worker-1", "sha256:" + strings.Repeat("b", 64), true}, {"worker-1", resultID, false}} {
		_, conflict := invoke(change.agent, change.result, change.approved)
		if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), first.JobID) {
			t.Fatalf("different intent did not retain owned original correlation: %d %s", conflict.Code, conflict.Body.String())
		}
	}
	if _, err = store.CreateStack(t.Context(), controlplane.CreateStackRequest{ID: "other-owned-stack", TenantID: "tenant-1", OwnerSubjectID: "auth0|user-1", Name: "Other", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	targetStack = "other-owned-stack"
	_, stackConflict := invoke("worker-1", resultID, true)
	if stackConflict.Code != http.StatusConflict || !strings.Contains(stackConflict.Body.String(), first.JobID) {
		t.Fatal("same key selected another stack instead of conflicting")
	}
}

// Whole-stack activation honors service locks; exact original-journal
// recovery remains available so a later lock cannot strand that operation.
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
	h := crudRouteHandlers{orch: orch, stackStore: store, jobStore: store, serviceStore: store, serverStore: store}

	for _, operation := range []string{"apply", "backup_restore_activate"} {
		event, recorder := stackLifecycleLockEvent(t, operation)
		if err := h.startStackKitLifecycle(event); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusConflict {
			t.Fatalf("apply over a locked service status = %d, want 409", recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), "service-locked") {
			t.Fatalf("refusal did not name the locked service: %s", recorder.Body.String())
		}
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
	resume, resumeRecorder := stackLifecycleLockEvent(t, "backup_restore_recover")
	if err := h.startStackKitLifecycle(resume); err != nil {
		t.Fatal(err)
	}
	if resumeRecorder.Code == http.StatusConflict && strings.Contains(resumeRecorder.Body.String(), "locked_service_ids") {
		t.Fatal("later service lock stranded original journal recovery")
	}
}
