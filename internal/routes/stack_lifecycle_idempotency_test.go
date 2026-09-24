package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	jobruntime "github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

type managedRuntimeManagerOutcome struct {
	result *jobruntime.ManagedLeaseResult
	err    error
}

type scriptedManagedRuntimeManager struct {
	mu                sync.Mutex
	preflightRequests []jobruntime.ManagedLeaseRequest
	requests          []jobruntime.ManagedLeaseRequest
	outcomes          []managedRuntimeManagerOutcome
	generation        uint64
	resolutionErr     error
}

func (m *scriptedManagedRuntimeManager) ResolveManagedRuntimeSlotGeneration(
	_ context.Context,
	request jobruntime.ManagedRuntimeSlotGenerationRequest,
) (jobruntime.ManagedRuntimeSlotGeneration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.resolutionErr != nil {
		return jobruntime.ManagedRuntimeSlotGeneration{}, m.resolutionErr
	}
	generation := m.generation
	if generation == 0 {
		generation = 1
	}
	return jobruntime.ManagedRuntimeSlotGeneration{
		RuntimeSlotID: providercontrol.DeriveManagedRuntimeSlotID(
			request.TenantID, request.StackID, request.RuntimeSlotKey,
		),
		GenerationOrdinal:  generation,
		ExistingUnreleased: generation == 1,
	}, nil
}

func (m *scriptedManagedRuntimeManager) PreflightCreateOrBindLease(_ context.Context, request jobruntime.ManagedLeaseRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.preflightRequests = append(m.preflightRequests, request)
	return nil
}

func (m *scriptedManagedRuntimeManager) CreateOrBindLease(_ context.Context, request jobruntime.ManagedLeaseRequest) (*jobruntime.ManagedLeaseResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, request)
	if len(m.outcomes) > 0 {
		outcome := m.outcomes[0]
		m.outcomes = m.outcomes[1:]
		if outcome.err != nil {
			return nil, outcome.err
		}
		if outcome.result != nil {
			result := *outcome.result
			return &result, nil
		}
	}
	suffix := request.OperationKey
	if len(suffix) > 12 {
		suffix = suffix[len(suffix)-12:]
	}
	return &jobruntime.ManagedLeaseResult{
		RuntimeSlotKey: request.RuntimeSlotKey,
		RuntimeSlotID: providercontrol.DeriveManagedRuntimeSlotID(
			request.TenantID, request.StackID, request.RuntimeSlotKey,
		),
		RuntimeSlotGeneration: request.RuntimeSlotGeneration,
		LeaseID:               "lease-" + suffix, RuntimeServerID: "server-" + suffix,
		ResourceGenerationID: "11111111-1111-4111-8111-" + suffix,
		OperationID:          "operation-" + suffix, Provider: request.Provider,
		DesiredState: "running", BillingMode: "subscription",
		Phase: jobruntime.RuntimePhaseLeasePending,
	}, nil
}

func (m *scriptedManagedRuntimeManager) snapshotRequests() []jobruntime.ManagedLeaseRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]jobruntime.ManagedLeaseRequest(nil), m.requests...)
}

type flakyManagedRuntimeJobStore struct {
	controlplane.JobStore
	mu                   sync.Mutex
	startFailures        int
	completeFailures     int
	commitBeforeComplete bool
}

func (s *flakyManagedRuntimeJobStore) StartJob(ctx context.Context, tenantID, jobID string, at time.Time) (*controlplane.Job, error) {
	s.mu.Lock()
	if s.startFailures > 0 {
		s.startFailures--
		s.mu.Unlock()
		return nil, errors.New("injected start failure")
	}
	s.mu.Unlock()
	return s.JobStore.StartJob(ctx, tenantID, jobID, at)
}

func (s *flakyManagedRuntimeJobStore) CompleteJob(ctx context.Context, tenantID, jobID string, result map[string]any, at time.Time) (*controlplane.Job, error) {
	s.mu.Lock()
	if s.completeFailures > 0 {
		s.completeFailures--
		commit := s.commitBeforeComplete
		s.mu.Unlock()
		if commit {
			if _, err := s.JobStore.CompleteJob(ctx, tenantID, jobID, result, at); err != nil {
				return nil, err
			}
		}
		return nil, errors.New("injected completion response loss")
	}
	s.mu.Unlock()
	return s.JobStore.CompleteJob(ctx, tenantID, jobID, result, at)
}

func TestManagedRuntimeIdempotencyKeyValidation(t *testing.T) {
	tests := []struct {
		name   string
		values []string
	}{
		{name: "missing"},
		{name: "empty", values: []string{""}},
		{name: "multiple", values: []string{"one", "two"}},
		{name: "combined", values: []string{"one,two"}},
		{name: "leading whitespace", values: []string{" key"}},
		{name: "trailing whitespace", values: []string{"key "}},
		{name: "control", values: []string{"key\x00value"}},
		{name: "overlength", values: []string{strings.Repeat("a", 257)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, _ := http.NewRequest(http.MethodPost, "http://example.test", nil)
			request.Header.Del(managedRuntimeIdempotencyHeader)
			for _, value := range test.values {
				request.Header.Add(managedRuntimeIdempotencyHeader, value)
			}
			if _, err := readManagedRuntimeIdempotencyKey(request); !errors.Is(err, errManagedRuntimeIdempotencyKeyInvalid) {
				t.Fatalf("error = %v, want invalid idempotency key", err)
			}
		})
	}
	for _, valid := range []string{"Case-Sensitive-Key", "opaque:key/with.symbols", strings.Repeat("x", 256)} {
		request, _ := http.NewRequest(http.MethodPost, "http://example.test", nil)
		request.Header.Set(managedRuntimeIdempotencyHeader, valid)
		if got, err := readManagedRuntimeIdempotencyKey(request); err != nil || got != valid {
			t.Fatalf("valid key %q = %q, %v", valid, got, err)
		}
	}
}

func TestManagedRuntimeEffectiveIntentUsesDesiredConfigBeforeObservedSummary(t *testing.T) {
	intent := newManagedRuntimeEffectiveIntent(
		"tenant-1",
		"owner-1",
		managedRuntimeStackRef{
			ID: "stack-1", Name: "Stack",
			Config: map[string]any{
				"runtime_offering_id": "monthly-runtime-standard",
				"provider_region":     "de/fra",
			},
			RuntimeSummary: map[string]any{
				"runtime_offering_id": "monthly-runtime-premium",
				"provider_region":     "de/txl",
			},
		},
		managedRuntimeServerRequest{ProviderID: "ionos"},
	)
	if intent.RuntimeOfferingID != "monthly-runtime-standard" ||
		intent.ProviderRegion != "de/fra" {
		t.Fatalf("effective intent = %+v, want desired config authority", intent)
	}
}

func TestAddManagedRuntimeServerRequiresKeyBeforeDurableWrite(t *testing.T) {
	handler, store, manager := newManagedRuntimeIdempotencyHarness(t, nil)
	event, recorder := managedRuntimeIdempotencyEvent("missing-key", `{"provider_id":"ionos"}`)
	event.Request.Header.Del(managedRuntimeIdempotencyHeader)
	if err := handler.addManagedRuntimeServer(event); err != nil {
		t.Fatalf("addManagedRuntimeServer: %v", err)
	}
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%s, want 422", recorder.Code, recorder.Body.String())
	}
	jobs, err := store.ListJobsByStack(context.Background(), "tenant-1", "stack-idempotency", 10)
	if err != nil || len(jobs) != 0 || len(manager.snapshotRequests()) != 0 {
		t.Fatalf("jobs=%#v manager_calls=%d err=%v, want no writes", jobs, len(manager.snapshotRequests()), err)
	}
}

func TestAddManagedRuntimeServerRejectsInvalidRoleAndServicesBeforeDurableWrite(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "unknown node role", body: `{"provider_id":"ionos","node_role":"workre"}`},
		{name: "node role alias", body: `{"provider_id":"ionos","node_role":"main"}`},
		{name: "service with comma", body: `{"provider_id":"ionos","services":["files,monitoring"]}`},
		{name: "service with whitespace", body: `{"provider_id":"ionos","services":[" files"]}`},
		{name: "overlength service", body: `{"provider_id":"ionos","services":["` + strings.Repeat("a", 129) + `"]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, store, manager := newManagedRuntimeIdempotencyHarness(t, nil)
			event, recorder := managedRuntimeIdempotencyEvent("invalid-request-key", test.body)
			if err := handler.addManagedRuntimeServer(event); err != nil {
				t.Fatalf("addManagedRuntimeServer: %v", err)
			}
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s, want 400", recorder.Code, recorder.Body.String())
			}
			jobs, err := store.ListJobsByStack(context.Background(), "tenant-1", "stack-idempotency", 10)
			if err != nil || len(jobs) != 0 || len(manager.snapshotRequests()) != 0 {
				t.Fatalf("jobs=%#v manager_calls=%d err=%v, want no durable write", jobs, len(manager.snapshotRequests()), err)
			}
		})
	}
}

func TestAddManagedRuntimeServerReplaysCanonicalIntentAndPreservesRawKeyPrivacy(t *testing.T) {
	handler, store, manager := newManagedRuntimeIdempotencyHarness(t, nil)
	firstEvent, firstRecorder := managedRuntimeIdempotencyEvent(
		"Sensitive-Raw-Key-Do-Not-Persist",
		`{"provider_id":"ionos","provider_region":"de/txl","node_role":"worker","services":["files","pocket-id"]}`,
	)
	if err := handler.addManagedRuntimeServer(firstEvent); err != nil {
		t.Fatalf("first add: %v", err)
	}
	first := decodeManagedRuntimeSuccess(t, firstRecorder)
	if first.RuntimeSlotKey != jobruntime.PrimaryManagedRuntimeSlotKey || first.RuntimeSlotID == "" {
		t.Fatalf("first response slot = %q/%q, want stable primary slot", first.RuntimeSlotKey, first.RuntimeSlotID)
	}

	// Simulate a process/handler reload after the browser lost its transport
	// key. The completed durable job, not manager memory, must own the replay.
	reloadedManager := &scriptedManagedRuntimeManager{}
	reloadedHandler := handler
	reloadedHandler.managedLeases = reloadedManager

	secondEvent, secondRecorder := managedRuntimeIdempotencyEvent(
		"Replacement-Key-After-202",
		`{"provider_id":"ionos","ionos_datacenter":"de-txl","node_role":"worker","services":["pocket_id","files","files"]}`,
	)
	if err := reloadedHandler.addManagedRuntimeServer(secondEvent); err != nil {
		t.Fatalf("replay add: %v", err)
	}
	second := decodeManagedRuntimeSuccess(t, secondRecorder)
	if first.JobID != second.JobID || first.RuntimeSlotKey != second.RuntimeSlotKey || first.RuntimeSlotID != second.RuntimeSlotID ||
		first.LeaseID != second.LeaseID || first.RuntimeServerID != second.RuntimeServerID ||
		first.ResourceGenerationID != second.ResourceGenerationID || first.OperationID != second.OperationID || !second.IdempotentReplay {
		t.Fatalf("first=%+v second=%+v, want exact replay correlations", first, second)
	}
	if calls, reloadedCalls := manager.snapshotRequests(), reloadedManager.snapshotRequests(); len(calls) != 1 || len(reloadedCalls) != 0 {
		t.Fatalf("manager calls before/after reload = %d/%d, want completed replay without second admission", len(calls), len(reloadedCalls))
	}
	job, err := store.GetJob(context.Background(), "tenant-1", first.JobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	encoded, _ := json.Marshal(job)
	if strings.Contains(string(encoded), "Sensitive-Raw-Key-Do-Not-Persist") ||
		strings.Contains(string(encoded), "Replacement-Key-After-202") {
		t.Fatalf("raw idempotency key leaked into durable job: %s", encoded)
	}
	if job.Result["client_request_sha256"] == "" || job.Result["effective_intent_sha256"] == "" {
		t.Fatalf("durable request digests missing: %#v", job.Result)
	}
}

func TestManagedRuntimeExpansionServiceConvergesWithHTTPReplay(t *testing.T) {
	handler, _, manager := newManagedRuntimeIdempotencyHarness(t, nil)
	event, recorder := managedRuntimeIdempotencyEvent("ignored-header-key", "")
	result, err := (&ManagedRuntimeExpansion{h: handler}).Execute(event, ManagedRuntimeExpansionRequest{
		StackID: "stack-idempotency", IdempotencyKey: "wizard-run-key",
		ProviderID: "ionos", ProviderRegion: "de/txl", NodeRole: "worker",
		Services: []string{"pocket-id", "files"},
	})
	if err != nil || result == nil || result.JobID == "" {
		t.Fatalf("Execute result=%+v err=%v", result, err)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("callable service wrote a success envelope: %s", recorder.Body.String())
	}

	replayEvent, replayRecorder := managedRuntimeIdempotencyEvent(
		"replacement-browser-key",
		`{"provider_id":"ionos","ionos_datacenter":"de-txl","node_role":"worker","services":["files","pocket_id"]}`,
	)
	if err := handler.addManagedRuntimeServer(replayEvent); err != nil {
		t.Fatalf("HTTP replay: %v", err)
	}
	replayed := decodeManagedRuntimeSuccess(t, replayRecorder)
	if replayed.JobID != result.JobID || replayed.LeaseID != result.LeaseID ||
		replayed.RuntimeServerID != result.RuntimeServerID || !replayed.IdempotentReplay {
		t.Fatalf("service=%+v replay=%+v, want one receipt-validated outcome", result, replayed)
	}
	if calls := manager.snapshotRequests(); len(calls) != 1 {
		t.Fatalf("native admission calls = %d, want one shared authority call", len(calls))
	}
}

func TestAddManagedRuntimeServerReleasedSlotStartsNewServerSideGeneration(t *testing.T) {
	handler, store, manager := newManagedRuntimeIdempotencyHarness(t, nil)
	firstEvent, firstRecorder := managedRuntimeIdempotencyEvent(
		"first-browser-key", `{"provider_id":"ionos"}`,
	)
	if err := handler.addManagedRuntimeServer(firstEvent); err != nil {
		t.Fatalf("first add: %v", err)
	}
	first := decodeManagedRuntimeSuccess(t, firstRecorder)

	manager.mu.Lock()
	manager.generation = 2
	manager.mu.Unlock()
	secondEvent, secondRecorder := managedRuntimeIdempotencyEvent(
		"replacement-browser-key", `{"provider_id":"ionos"}`,
	)
	if err := handler.addManagedRuntimeServer(secondEvent); err != nil {
		t.Fatalf("replacement add: %v", err)
	}
	second := decodeManagedRuntimeSuccess(t, secondRecorder)
	if first.RuntimeSlotID != second.RuntimeSlotID {
		t.Fatalf("slot changed across replacement: first=%q second=%q", first.RuntimeSlotID, second.RuntimeSlotID)
	}
	if first.JobID == second.JobID || first.LeaseID == second.LeaseID ||
		first.RuntimeServerID == second.RuntimeServerID || first.OperationID == second.OperationID {
		t.Fatalf("replacement reused generation identity: first=%+v second=%+v", first, second)
	}
	jobs, err := store.ListJobsByStack(context.Background(), "tenant-1", "stack-idempotency", 10)
	requests := manager.snapshotRequests()
	if err != nil || len(jobs) != 2 || len(requests) != 2 ||
		requests[0].RuntimeSlotGeneration != 1 || requests[1].RuntimeSlotGeneration != 2 {
		t.Fatalf("jobs=%d requests=%+v err=%v, want one job per server-side generation", len(jobs), requests, err)
	}
}

func TestAddManagedRuntimeServerRejectsSameSlotDifferentIntentEvenWithNewKey(t *testing.T) {
	for _, test := range []struct {
		name       string
		firstBody  string
		secondBody string
	}{
		{
			name:       "services changed",
			firstBody:  `{"provider_id":"ionos","services":["files"]}`,
			secondBody: `{"provider_id":"ionos","services":["monitoring"]}`,
		},
		{
			name:       "provider swap in provider-neutral primary slot",
			firstBody:  `{"provider_id":"ionos"}`,
			secondBody: `{"provider_id":"centron"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, store, manager := newManagedRuntimeIdempotencyHarness(t, nil)
			firstEvent, firstRecorder := managedRuntimeIdempotencyEvent("conflicting-key", test.firstBody)
			if err := handler.addManagedRuntimeServer(firstEvent); err != nil || firstRecorder.Code != http.StatusAccepted {
				t.Fatalf("first add status=%d err=%v body=%s", firstRecorder.Code, err, firstRecorder.Body.String())
			}
			secondEvent, secondRecorder := managedRuntimeIdempotencyEvent("replacement-key", test.secondBody)
			if err := handler.addManagedRuntimeServer(secondEvent); err != nil {
				t.Fatalf("conflicting add: %v", err)
			}
			details := decodeErrorDetails(t, secondRecorder)
			if secondRecorder.Code != http.StatusConflict || details["reason_code"] != "idempotency_conflict" {
				t.Fatalf("status=%d details=%#v, want stable 409", secondRecorder.Code, details)
			}
			jobs, _ := store.ListJobsByStack(context.Background(), "tenant-1", "stack-idempotency", 10)
			if len(jobs) != 1 || len(manager.snapshotRequests()) != 1 {
				t.Fatalf("jobs=%d manager_calls=%d, want no conflicting mutation", len(jobs), len(manager.snapshotRequests()))
			}
		})
	}
}

func TestAddManagedRuntimeServerDoesNotDiscloseReplayToDifferentOwner(t *testing.T) {
	handler, _, manager := newManagedRuntimeIdempotencyHarness(t, nil)
	firstEvent, firstRecorder := managedRuntimeIdempotencyEvent("owner-scoped-key", `{"provider_id":"ionos"}`)
	if err := handler.addManagedRuntimeServer(firstEvent); err != nil || firstRecorder.Code != http.StatusAccepted {
		t.Fatalf("first status=%d err=%v body=%s", firstRecorder.Code, err, firstRecorder.Body.String())
	}
	foreignEvent, foreignRecorder := stackLifecycleRouteTestEvent(
		http.MethodPost,
		"/api/v1/stacks/stack-idempotency/managed-runtimes",
		`{"provider_id":"ionos"}`,
		"owner-2",
		"tenant-1",
	)
	foreignEvent.Request.SetPathValue("id", "stack-idempotency")
	foreignEvent.Request.Header.Set(managedRuntimeIdempotencyHeader, "owner-scoped-key")
	if err := handler.addManagedRuntimeServer(foreignEvent); err != nil {
		t.Fatalf("foreign replay: %v", err)
	}
	if foreignRecorder.Code != http.StatusForbidden || strings.Contains(foreignRecorder.Body.String(), "lease-") ||
		strings.Contains(foreignRecorder.Body.String(), "operation-") {
		t.Fatalf("status=%d body=%s, want authorization failure without replay disclosure", foreignRecorder.Code, foreignRecorder.Body.String())
	}
	if len(manager.snapshotRequests()) != 1 {
		t.Fatalf("manager calls=%d, foreign replay must not mutate", len(manager.snapshotRequests()))
	}
}

func TestAddManagedRuntimeServerResumesPendingAndUnconfirmedAdmission(t *testing.T) {
	manager := &scriptedManagedRuntimeManager{outcomes: []managedRuntimeManagerOutcome{{err: errors.New("commit response lost")}}}
	baseStore := controlplane.NewMemoryStore()
	flakyStore := &flakyManagedRuntimeJobStore{JobStore: baseStore, startFailures: 1}
	handler, _, _ := newManagedRuntimeIdempotencyHarnessWithStores(t, baseStore, flakyStore, manager)

	firstEvent, firstRecorder := managedRuntimeIdempotencyEvent("crash-safe-key", `{"provider_id":"ionos"}`)
	if err := handler.addManagedRuntimeServer(firstEvent); err != nil {
		t.Fatalf("first pending attempt: %v", err)
	}
	if firstRecorder.Code != http.StatusServiceUnavailable || len(manager.snapshotRequests()) != 0 {
		t.Fatalf("first status=%d manager_calls=%d body=%s", firstRecorder.Code, len(manager.snapshotRequests()), firstRecorder.Body.String())
	}

	secondEvent, secondRecorder := managedRuntimeIdempotencyEvent("crash-safe-key", `{"provider_id":"ionos"}`)
	if err := handler.addManagedRuntimeServer(secondEvent); err != nil {
		t.Fatalf("second running attempt: %v", err)
	}
	secondDetails := decodeErrorDetails(t, secondRecorder)
	if secondRecorder.Code != http.StatusServiceUnavailable || secondDetails["reason_code"] != "native_admission_outcome_unconfirmed" || secondDetails["retryable"] != true {
		t.Fatalf("second status=%d details=%#v, want recoverable uncertainty", secondRecorder.Code, secondDetails)
	}

	thirdEvent, thirdRecorder := managedRuntimeIdempotencyEvent("crash-safe-key", `{"provider_id":"ionos"}`)
	if err := handler.addManagedRuntimeServer(thirdEvent); err != nil {
		t.Fatalf("third resume: %v", err)
	}
	third := decodeManagedRuntimeSuccess(t, thirdRecorder)
	if !third.IdempotentReplay || len(manager.snapshotRequests()) != 2 {
		t.Fatalf("response=%+v manager_calls=%d, want same-key running resume", third, len(manager.snapshotRequests()))
	}
	job, err := baseStore.GetJob(context.Background(), "tenant-1", third.JobID)
	if err != nil || job.State != managedRuntimeJobStateCompleted {
		t.Fatalf("job=%+v err=%v, want completed after resume", job, err)
	}
}

func TestAddManagedRuntimeServerCompletionLossReconcilesSameJob(t *testing.T) {
	for _, test := range []struct {
		name                 string
		commitBeforeResponse bool
	}{
		{name: "completion not committed"},
		{name: "completion committed response lost", commitBeforeResponse: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := &scriptedManagedRuntimeManager{}
			baseStore := controlplane.NewMemoryStore()
			flakyStore := &flakyManagedRuntimeJobStore{
				JobStore: baseStore, completeFailures: 1, commitBeforeComplete: test.commitBeforeResponse,
			}
			handler, _, _ := newManagedRuntimeIdempotencyHarnessWithStores(t, baseStore, flakyStore, manager)
			firstEvent, firstRecorder := managedRuntimeIdempotencyEvent("completion-key", `{"provider_id":"ionos"}`)
			if err := handler.addManagedRuntimeServer(firstEvent); err != nil {
				t.Fatalf("first add: %v", err)
			}
			first := decodeManagedRuntimeSuccess(t, firstRecorder)
			if test.commitBeforeResponse {
				if !first.IdempotentReplay {
					t.Fatalf("response=%+v, committed completion must be reloaded as replay", first)
				}
				return
			}
			if len(first.Warnings) != 1 || strings.TrimSpace(first.Warnings[0]) == "" {
				t.Fatalf("response=%+v, want retry guidance", first)
			}
			secondEvent, secondRecorder := managedRuntimeIdempotencyEvent("completion-key", `{"provider_id":"ionos"}`)
			if err := handler.addManagedRuntimeServer(secondEvent); err != nil {
				t.Fatalf("completion retry: %v", err)
			}
			second := decodeManagedRuntimeSuccess(t, secondRecorder)
			if second.JobID != first.JobID || second.LeaseID != first.LeaseID || !second.IdempotentReplay {
				t.Fatalf("first=%+v second=%+v, want exact completion resume", first, second)
			}
		})
	}
}

func TestAddManagedRuntimeServerCompletedReplayBypassesChangedEntitlement(t *testing.T) {
	handler, _, manager := newManagedRuntimeIdempotencyHarness(t, nil)
	firstEvent, firstRecorder := managedRuntimeIdempotencyEvent("entitlement-replay", `{"provider_id":"ionos"}`)
	if err := handler.addManagedRuntimeServer(firstEvent); err != nil || firstRecorder.Code != http.StatusAccepted {
		t.Fatalf("first status=%d err=%v body=%s", firstRecorder.Code, err, firstRecorder.Body.String())
	}
	handler.features = stackLifecycleFeatureChecker{enabled: false}
	secondEvent, secondRecorder := managedRuntimeIdempotencyEvent("entitlement-replay", `{"provider_id":"ionos"}`)
	if err := handler.addManagedRuntimeServer(secondEvent); err != nil {
		t.Fatalf("replay: %v", err)
	}
	second := decodeManagedRuntimeSuccess(t, secondRecorder)
	if !second.IdempotentReplay || len(manager.snapshotRequests()) != 1 {
		t.Fatalf("response=%+v manager_calls=%d, replay must precede changed entitlement", second, len(manager.snapshotRequests()))
	}
}

func TestAddManagedRuntimeServerPendingReplayRechecksEntitlement(t *testing.T) {
	manager := &scriptedManagedRuntimeManager{}
	baseStore := controlplane.NewMemoryStore()
	flakyStore := &flakyManagedRuntimeJobStore{JobStore: baseStore, startFailures: 1}
	handler, _, _ := newManagedRuntimeIdempotencyHarnessWithStores(t, baseStore, flakyStore, manager)

	firstEvent, firstRecorder := managedRuntimeIdempotencyEvent("pending-entitlement-key", `{"provider_id":"ionos"}`)
	if err := handler.addManagedRuntimeServer(firstEvent); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if firstRecorder.Code != http.StatusServiceUnavailable || len(manager.snapshotRequests()) != 0 {
		t.Fatalf("first status=%d manager_calls=%d body=%s, want unclaimed pending job", firstRecorder.Code, len(manager.snapshotRequests()), firstRecorder.Body.String())
	}

	handler.features = stackLifecycleFeatureChecker{enabled: false}
	replayEvent, replayRecorder := managedRuntimeIdempotencyEvent("pending-entitlement-key", `{"provider_id":"ionos"}`)
	if err := handler.addManagedRuntimeServer(replayEvent); err != nil {
		t.Fatalf("pending replay: %v", err)
	}
	if replayRecorder.Code != http.StatusForbidden || len(manager.snapshotRequests()) != 0 {
		t.Fatalf("replay status=%d manager_calls=%d body=%s, want entitlement denial before admission", replayRecorder.Code, len(manager.snapshotRequests()), replayRecorder.Body.String())
	}
	jobs, err := baseStore.ListJobsByStack(context.Background(), "tenant-1", "stack-idempotency", 10)
	if err != nil || len(jobs) != 1 || jobs[0].State != monthlyRuntimeEnrollmentStatusPending {
		t.Fatalf("jobs=%#v err=%v, want one still-pending job", jobs, err)
	}
}

func TestAddManagedRuntimeServerRejectsSubstitutedCompletedCorrelations(t *testing.T) {
	handler, store, manager := newManagedRuntimeIdempotencyHarness(t, nil)
	firstEvent, firstRecorder := managedRuntimeIdempotencyEvent("sealed-correlation-key", `{"provider_id":"ionos"}`)
	if err := handler.addManagedRuntimeServer(firstEvent); err != nil {
		t.Fatalf("first add: %v", err)
	}
	first := decodeManagedRuntimeSuccess(t, firstRecorder)
	job, err := store.GetJob(context.Background(), "tenant-1", first.JobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	tampered := cloneLifecycleMap(job.Result)
	tampered["lease_id"] = "lease-substituted"
	tampered["runtime_server_id"] = "server-substituted"
	tampered["resource_generation_id"] = "22222222-2222-4222-8222-222222222222"
	tampered["operation_id"] = "operation-substituted"
	if _, err = store.UpsertJob(context.Background(), controlplane.UpsertJobRequest{
		ID: job.ID, TenantID: job.TenantID, StackID: job.StackID, Type: job.Type,
		State: managedRuntimeJobStateCompleted, Progress: 100, Result: tampered,
	}); err != nil {
		t.Fatalf("inject substituted generic job projection: %v", err)
	}

	replayEvent, replayRecorder := managedRuntimeIdempotencyEvent("sealed-correlation-key", `{"provider_id":"ionos"}`)
	if err = handler.addManagedRuntimeServer(replayEvent); err != nil {
		t.Fatalf("replay: %v", err)
	}
	replayDetails := decodeErrorDetails(t, replayRecorder)
	if replayRecorder.Code != http.StatusServiceUnavailable || replayDetails["reason_code"] != "idempotency_state_invalid" ||
		strings.Contains(replayRecorder.Body.String(), "substituted") {
		t.Fatalf("status=%d details=%#v, want fail-closed sealed replay", replayRecorder.Code, replayDetails)
	}
	if len(manager.snapshotRequests()) != 1 {
		t.Fatalf("manager calls=%d, substituted completion must not be admitted or returned", len(manager.snapshotRequests()))
	}
}

func TestManagedRuntimeSlotIdentityFollowsExplicitSlotInsteadOfTransportKey(t *testing.T) {
	for _, tc := range []struct {
		name      string
		keys      []string
		bodies    []string
		wantSlots []string
	}{
		{
			name: "transport keys resume the default slot", keys: []string{"Case-Key", "case-key"},
			bodies:    []string{`{"provider_id":"ionos"}`, `{"provider_id":"ionos"}`},
			wantSlots: []string{jobruntime.PrimaryManagedRuntimeSlotKey},
		},
		{
			name: "explicit slots create distinct intent", keys: []string{"transport-key-worker-1", "transport-key-worker-2"},
			bodies:    []string{`{"provider_id":"ionos","runtime_slot_key":"worker-1"}`, `{"provider_id":"ionos","runtime_slot_key":"worker-2"}`},
			wantSlots: []string{"worker-1", "worker-2"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, store, manager := newManagedRuntimeIdempotencyHarness(t, nil)
			for index := range tc.keys {
				event, recorder := managedRuntimeIdempotencyEvent(tc.keys[index], tc.bodies[index])
				if err := handler.addManagedRuntimeServer(event); err != nil || recorder.Code != http.StatusAccepted {
					t.Fatalf("request=%d status=%d err=%v body=%s", index, recorder.Code, err, recorder.Body.String())
				}
			}
			jobs, _ := store.ListJobsByStack(context.Background(), "tenant-1", "stack-idempotency", 10)
			requests := manager.snapshotRequests()
			if len(jobs) != len(tc.wantSlots) || len(requests) != len(tc.wantSlots) {
				t.Fatalf("jobs=%d requests=%+v, want slots %v", len(jobs), requests, tc.wantSlots)
			}
			for index, want := range tc.wantSlots {
				if requests[index].RuntimeSlotKey != want {
					t.Fatalf("request %d slot=%q, want %q", index, requests[index].RuntimeSlotKey, want)
				}
			}
		})
	}
}

func TestAddManagedRuntimeServerConcurrentSameKeyConverges(t *testing.T) {
	handler, store, manager := newManagedRuntimeIdempotencyHarness(t, nil)
	type outcome struct {
		response ManagedRuntimeExpansionResult
		status   int
		err      error
	}
	outcomes := make(chan outcome, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			event, recorder := managedRuntimeIdempotencyEvent(
				"concurrent-key",
				`{"provider_id":"ionos","services":["files","monitoring"]}`,
			)
			err := handler.addManagedRuntimeServer(event)
			var envelope struct {
				Data ManagedRuntimeExpansionResult `json:"data"`
			}
			_ = json.Unmarshal(recorder.Body.Bytes(), &envelope)
			outcomes <- outcome{response: envelope.Data, status: recorder.Code, err: err}
		}()
	}
	wait.Wait()
	close(outcomes)
	results := make([]outcome, 0, 2)
	for result := range outcomes {
		results = append(results, result)
	}
	if len(results) != 2 || results[0].err != nil || results[1].err != nil ||
		results[0].status != http.StatusAccepted || results[1].status != http.StatusAccepted {
		t.Fatalf("outcomes=%+v, want two accepted responses", results)
	}
	left, right := results[0].response, results[1].response
	if left.JobID == "" || left.JobID != right.JobID || left.LeaseID != right.LeaseID ||
		left.RuntimeServerID != right.RuntimeServerID || left.OperationID != right.OperationID {
		t.Fatalf("left=%+v right=%+v, want exact convergence", left, right)
	}
	jobs, _ := store.ListJobsByStack(context.Background(), "tenant-1", "stack-idempotency", 10)
	requests := manager.snapshotRequests()
	if len(jobs) != 1 || len(requests) < 1 || len(requests) > 2 {
		t.Fatalf("jobs=%d manager_calls=%d, want one durable job and bounded replays", len(jobs), len(requests))
	}
	for _, request := range requests {
		if request.OperationKey != left.JobID {
			t.Fatalf("operation key = %q, want deterministic job %q", request.OperationKey, left.JobID)
		}
	}
}

func newManagedRuntimeIdempotencyHarness(t *testing.T, manager *scriptedManagedRuntimeManager) (stackLifecycleRouteHandlers, *controlplane.MemoryStore, *scriptedManagedRuntimeManager) {
	t.Helper()
	store := controlplane.NewMemoryStore()
	return newManagedRuntimeIdempotencyHarnessWithStores(t, store, store, manager)
}

func TestAddManagedRuntimeCapacityRecheckDenialKeepsRunningSameKeyReconciliation(t *testing.T) {
	manager := &scriptedManagedRuntimeManager{outcomes: []managedRuntimeManagerOutcome{{
		err: &providercontrol.ManagedRuntimeCapacityExceededError{
			TenantID: "tenant-1", OwnerSubjectID: "owner-1", Limit: 3, Held: 3,
		},
	}}}
	handler, store, _ := newManagedRuntimeIdempotencyHarness(t, manager)
	body := `{"provider_id":"ionos","node_role":"worker"}`

	firstEvent, firstRecorder := managedRuntimeIdempotencyEvent("capacity-denial-key", body)
	if err := handler.addManagedRuntimeServer(firstEvent); err != nil {
		t.Fatalf("first capacity denial: %v", err)
	}
	firstDetails := decodeErrorDetails(t, firstRecorder)
	if firstRecorder.Code != http.StatusForbidden || firstDetails["reason_code"] != monthlyruntime.ReasonMaxServersReached ||
		firstDetails["reserved_servers"] != float64(3) || firstDetails["job_state"] != "running" ||
		firstDetails["retry_scope"] != "exact_same_idempotency_key" {
		t.Fatalf("first denial status=%d details=%#v", firstRecorder.Code, firstDetails)
	}
	if _, exists := firstDetails["active_servers"]; exists {
		t.Fatalf("first denial exposed obsolete active_servers: %#v", firstDetails)
	}

	jobs, err := store.ListJobsByStack(context.Background(), "tenant-1", "stack-idempotency", 10)
	if err != nil || len(jobs) != 1 || jobs[0].State != "running" || firstDetails[managedRuntimeDetailsJobIDKey] != jobs[0].ID {
		t.Fatalf("running reconciliation fence jobs=%#v err=%v details=%#v", jobs, err, firstDetails)
	}

	replayEvent, replayRecorder := managedRuntimeIdempotencyEvent("capacity-denial-key", body)
	if err := handler.addManagedRuntimeServer(replayEvent); err != nil {
		t.Fatalf("capacity denial replay: %v", err)
	}
	if replayRecorder.Code != http.StatusAccepted {
		t.Fatalf("same-key reconciliation status=%d body=%s, want accepted", replayRecorder.Code, replayRecorder.Body.String())
	}
	jobs, err = store.ListJobsByStack(context.Background(), "tenant-1", "stack-idempotency", 10)
	requests := manager.snapshotRequests()
	if err != nil || len(jobs) != 1 || jobs[0].State != managedRuntimeJobStateCompleted || len(requests) != 2 ||
		requests[0].OperationKey != jobs[0].ID || requests[1].OperationKey != jobs[0].ID {
		t.Fatalf("jobs=%#v requests=%#v err=%v, want one completed exact same-key reconciliation", jobs, requests, err)
	}
}

func TestAddManagedRuntimeCapacityDenialRejectsMismatchedAuthorityScope(t *testing.T) {
	manager := &scriptedManagedRuntimeManager{outcomes: []managedRuntimeManagerOutcome{{
		err: &providercontrol.ManagedRuntimeCapacityExceededError{
			TenantID: "tenant-1", OwnerSubjectID: "different-owner", Limit: 3, Held: 3,
		},
	}}}
	handler, store, _ := newManagedRuntimeIdempotencyHarness(t, manager)
	event, recorder := managedRuntimeIdempotencyEvent(
		"capacity-scope-mismatch-key", `{"provider_id":"ionos","node_role":"worker"}`,
	)
	if err := handler.addManagedRuntimeServer(event); err != nil {
		t.Fatalf("mismatched capacity denial: %v", err)
	}
	details := decodeErrorDetails(t, recorder)
	if recorder.Code != http.StatusServiceUnavailable || details["reason_code"] != "idempotency_state_invalid" {
		t.Fatalf("mismatched denial status=%d details=%#v", recorder.Code, details)
	}
	jobs, err := store.ListJobsByStack(context.Background(), "tenant-1", "stack-idempotency", 10)
	if err != nil || len(jobs) != 1 || jobs[0].State == monthlyRuntimeEnrollmentStatusFailed {
		t.Fatalf("scope mismatch must not persist a terminal capacity denial: jobs=%#v err=%v", jobs, err)
	}
}

func newManagedRuntimeIdempotencyHarnessWithStores(
	t *testing.T,
	stackStore *controlplane.MemoryStore,
	jobStore controlplane.JobStore,
	manager *scriptedManagedRuntimeManager,
) (stackLifecycleRouteHandlers, *controlplane.MemoryStore, *scriptedManagedRuntimeManager) {
	t.Helper()
	if manager == nil {
		manager = &scriptedManagedRuntimeManager{}
	}
	if _, err := stackStore.CreateStack(context.Background(), controlplane.CreateStackRequest{
		ID: "stack-idempotency", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Idempotency Stack", Status: "running",
		Config: map[string]any{
			"server_provisioning_mode": "kombify-cloud", "runtime_lane": "monthly-runtime",
			"runtime_offering_id": "monthly-runtime-standard", "stackkit_catalog_ref": "cloud-kit",
			"provider_id": "ionos", "provider_region": "de/fra",
		},
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{
		Now: func() time.Time { return time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC) }, SnapshotSecret: []byte("secret"),
	})
	return stackLifecycleRouteHandlers{
		leases: leases, features: stackLifecycleFeatureChecker{enabled: true},
		stacks: stackStore, jobs: jobStore, managedLeases: manager,
	}, stackStore, manager
}

func managedRuntimeIdempotencyEvent(key, body string) (*httpx.Event, *httptest.ResponseRecorder) {
	event, recorder := stackLifecycleRouteTestEvent(
		http.MethodPost, "/api/v1/stacks/stack-idempotency/managed-runtimes", body, "owner-1", "tenant-1",
	)
	event.Request.SetPathValue("id", "stack-idempotency")
	event.Request.Header.Set(managedRuntimeIdempotencyHeader, key)
	return event, recorder
}

func decodeManagedRuntimeSuccess(t *testing.T, recorder *httptest.ResponseRecorder) ManagedRuntimeExpansionResult {
	t.Helper()
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s, want 202", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data ManagedRuntimeExpansionResult `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.JobID == "" {
		t.Fatalf("missing job response: %s", recorder.Body.String())
	}
	return envelope.Data
}

var (
	_ jobruntime.ManagedLeaseManager = (*scriptedManagedRuntimeManager)(nil)
	_ controlplane.JobStore          = (*flakyManagedRuntimeJobStore)(nil)
)
