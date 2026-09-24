package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	jobruntime "github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

type stackLifecycleFeatureChecker struct {
	enabled bool
}

type stackLifecycleManagedLeaseManager struct {
	preflightRequests []jobruntime.ManagedLeaseRequest
	preflightErr      error
	requests          []jobruntime.ManagedLeaseRequest
	result            *jobruntime.ManagedLeaseResult
	err               error
}

func (m *stackLifecycleManagedLeaseManager) ResolveManagedRuntimeSlotGeneration(
	_ context.Context,
	req jobruntime.ManagedRuntimeSlotGenerationRequest,
) (jobruntime.ManagedRuntimeSlotGeneration, error) {
	return jobruntime.ManagedRuntimeSlotGeneration{
		RuntimeSlotID: providercontrol.DeriveManagedRuntimeSlotID(
			req.TenantID, req.StackID, req.RuntimeSlotKey,
		),
		GenerationOrdinal: 1,
	}, nil
}

func (m *stackLifecycleManagedLeaseManager) PreflightCreateOrBindLease(_ context.Context, req jobruntime.ManagedLeaseRequest) error {
	m.preflightRequests = append(m.preflightRequests, req)
	return m.preflightErr
}

func (m *stackLifecycleManagedLeaseManager) CreateOrBindLease(_ context.Context, req jobruntime.ManagedLeaseRequest) (*jobruntime.ManagedLeaseResult, error) {
	m.requests = append(m.requests, req)
	if m.err != nil {
		return nil, m.err
	}
	if m.result != nil {
		copy := *m.result
		return &copy, nil
	}
	return &jobruntime.ManagedLeaseResult{
		RuntimeSlotKey: req.RuntimeSlotKey,
		RuntimeSlotID: providercontrol.DeriveManagedRuntimeSlotID(
			req.TenantID, req.StackID, req.RuntimeSlotKey,
		),
		RuntimeSlotGeneration: req.RuntimeSlotGeneration,
		LeaseID:               "lease-native-1",
		RuntimeServerID:       "server-native-1",
		ResourceGenerationID:  "11111111-1111-4111-8111-111111111111",
		OperationID:           "operation-native-1",
		Provider:              req.Provider,
		DesiredState:          "running",
		BillingMode:           "subscription",
		Phase:                 jobruntime.RuntimePhaseLeasePending,
	}, nil
}

func (f stackLifecycleFeatureChecker) IsEnabled(context.Context, string, string) (bool, error) {
	return f.enabled, nil
}

func TestReadManagedRuntimeServerRequestDefaultsSlotToPrimaryIndependentOfRole(t *testing.T) {
	for _, role := range []string{"foundation", "worker", "storage"} {
		t.Run(role, func(t *testing.T) {
			event, _ := stackLifecycleRouteTestEvent(
				http.MethodPost,
				"/api/v1/stacks/stack-1/managed-runtimes",
				`{"provider_id":"ionos","node_role":"`+role+`"}`,
				"owner-1",
				"tenant-1",
			)
			request, err := readManagedRuntimeServerRequest(event)
			if err != nil {
				t.Fatalf("readManagedRuntimeServerRequest: %v", err)
			}
			if request.RuntimeSlotKey != jobruntime.PrimaryManagedRuntimeSlotKey || request.NodeRole != role {
				t.Fatalf("slot/role = %q/%q, want primary/%q", request.RuntimeSlotKey, request.NodeRole, role)
			}
		})
	}
}

func TestAddManagedRuntimeServerRejectsMissingEntitlementBeforeLeaseCreation(t *testing.T) {
	store := controlplane.NewMemoryStore()
	stack, err := store.CreateStack(context.Background(), controlplane.CreateStackRequest{
		ID: "stack-entitlement", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Status: "pending",
		Config: map[string]any{"server_provisioning_mode": "kombify-cloud", "server_mode": "monthly-runtime", "runtime_lane": "monthly-runtime", "runtime_offering_id": "monthly-runtime-standard", "lease_provider": "centron-managed"},
	})
	if err != nil {
		t.Fatalf("seed stack: %v", err)
	}
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{
		Now:            func() time.Time { return time.Date(2026, 5, 26, 8, 0, 0, 0, time.UTC) },
		SnapshotSecret: []byte("secret"),
	})

	body := `{"node_role":"storage","provider_id":"ionos","ionos_datacenter":"us/ewr","runtime_offering_id":"monthly-runtime-premium","stackkit":"basement-kit","services":["files","files"]}`
	event, recorder := stackLifecycleRouteTestEvent(http.MethodPost, "/api/v1/stacks/"+stack.ID+"/managed-runtimes", body, "owner-1", "tenant-1")
	event.Request.SetPathValue("id", stack.ID)
	if err := (stackLifecycleRouteHandlers{leases: leases, features: stackLifecycleFeatureChecker{enabled: false}, stacks: store, jobs: store}).addManagedRuntimeServer(event); err != nil {
		t.Fatalf("addManagedRuntimeServer returned router error: %v", err)
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403", recorder.Code, recorder.Body.String())
	}
	list, err := leases.ListByTenant(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("leases = %d, want none after entitlement rejection", len(list))
	}
	jobs, err := store.ListJobsByStack(context.Background(), "tenant-1", stack.ID, 100)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("jobs = %d, want none before entitlement succeeds", len(jobs))
	}
}

func TestAddManagedRuntimeServerDoesNotPromoteHistoricalProviderAlias(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(context.Background(), controlplane.CreateStackRequest{
		ID:             "stack-historical-provider",
		TenantID:       "tenant-1",
		OwnerSubjectID: "owner-1",
		Name:           "Historical Provider",
		Status:         "running",
		Config: map[string]any{
			"server_provisioning_mode": "kombify-cloud",
			"server_mode":              "monthly-runtime",
			"runtime_lane":             "monthly-runtime",
			"lease_provider":           "ionos-managed",
		},
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{
		Now:            func() time.Time { return time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC) },
		SnapshotSecret: []byte("secret"),
	})
	event, recorder := stackLifecycleRouteTestEvent(
		http.MethodPost,
		"/api/v1/stacks/stack-historical-provider/managed-runtimes",
		`{"node_role":"worker"}`,
		"owner-1",
		"tenant-1",
	)
	event.Request.SetPathValue("id", "stack-historical-provider")

	if err := (stackLifecycleRouteHandlers{
		leases: leases, features: stackLifecycleFeatureChecker{enabled: true}, stacks: store, jobs: store,
	}).addManagedRuntimeServer(event); err != nil {
		t.Fatalf("addManagedRuntimeServer returned router error: %v", err)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want explicit provider_id rejection", recorder.Code, recorder.Body.String())
	}
	created, err := leases.ListByTenant(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(created) != 0 {
		t.Fatalf("leases = %#v, want no write", created)
	}
	jobs, err := store.ListJobsByStack(context.Background(), "tenant-1", "stack-historical-provider", 10)
	if err != nil {
		t.Fatalf("ListJobsByStack: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("jobs = %#v, want no write", jobs)
	}
}

func TestAddManagedRuntimeServerControlPlaneStackFailsClosedWithoutNativeAdmission(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(context.Background(), controlplane.CreateStackRequest{
		ID:             "stack-store-managed",
		TenantID:       "tenant-1",
		OwnerSubjectID: "owner-1",
		Name:           "Store Managed",
		Status:         "running",
		Config: map[string]any{
			"server_provisioning_mode": "kombify-cloud",
			"server_mode":              "monthly-runtime",
			"runtime_lane":             "monthly-runtime",
			"runtime_offering_id":      "monthly-runtime-standard",
			"lease_provider":           "centron-managed",
			"stackkit_catalog_ref":     "cloud-kit",
		},
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{
		Now:            func() time.Time { return time.Date(2026, 5, 26, 8, 0, 0, 0, time.UTC) },
		SnapshotSecret: []byte("secret"),
	})

	body := `{"node_role":"worker","provider_id":"ionos","provider_region":"de-txl","runtime_offering_id":"monthly-runtime-premium","stackkit":"cloud-kit","services":["files","monitoring"]}`
	event, recorder := stackLifecycleRouteTestEvent(http.MethodPost, "/api/v1/stacks/stack-store-managed/managed-runtimes", body, "owner-1", "tenant-1")
	event.Request.SetPathValue("id", "stack-store-managed")
	if err := (stackLifecycleRouteHandlers{
		leases:   leases,
		features: stackLifecycleFeatureChecker{enabled: true},
		stacks:   store,
		jobs:     store,
	}).addManagedRuntimeServer(event); err != nil {
		t.Fatalf("addManagedRuntimeServer returned router error: %v", err)
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s, want fail-closed 503", recorder.Code, recorder.Body.String())
	}
	var env struct {
		Error struct {
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error body %q: %v", recorder.Body.String(), err)
	}
	if env.Error.Details["reason_code"] != "native_admission_unavailable" || env.Error.Details["retryable"] != true {
		t.Fatalf("details = %#v, want retryable native_admission_unavailable", env.Error.Details)
	}
	list, err := leases.ListByTenant(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("leases = %d, want no placeholder lease before native admission", len(list))
	}
	updated, err := store.GetStack(context.Background(), "tenant-1", "stack-store-managed")
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	if updated.RuntimeSummary["lease_id"] != nil {
		t.Fatalf("runtime summary = %#v, want no placeholder primary lease", updated.RuntimeSummary)
	}
}

func TestAddManagedRuntimeServerUsesNativeAdmissionWithJobOperationKey(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(context.Background(), controlplane.CreateStackRequest{
		ID:             "stack-native-add",
		TenantID:       "tenant-1",
		OwnerSubjectID: "owner-1",
		Name:           "Native Add",
		Status:         "running",
		Config: map[string]any{
			"server_provisioning_mode": "kombify-cloud",
			"runtime_lane":             "monthly-runtime",
			"runtime_offering_id":      "monthly-runtime-standard",
			"stackkit_catalog_ref":     "cloud-kit",
			"provider_id":              "ionos",
		},
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	legacyLeases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{
		Now:            func() time.Time { return time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC) },
		SnapshotSecret: []byte("secret"),
	})
	managedLeases := &stackLifecycleManagedLeaseManager{}
	event, recorder := stackLifecycleRouteTestEvent(
		http.MethodPost,
		"/api/v1/stacks/stack-native-add/managed-runtimes",
		`{"node_role":"storage","provider_id":"ionos","provider_region":"de-txl","services":["files","pocket-id","files"]}`,
		"owner-1",
		"tenant-1",
	)
	event.Request.SetPathValue("id", "stack-native-add")
	if err := (stackLifecycleRouteHandlers{
		leases: legacyLeases, managedLeases: managedLeases,
		features: stackLifecycleFeatureChecker{enabled: true}, stacks: store, jobs: store,
	}).addManagedRuntimeServer(event); err != nil {
		t.Fatalf("addManagedRuntimeServer returned router error: %v", err)
	}
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s, want 202", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data ManagedRuntimeExpansionResult `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.JobID == "" || envelope.Data.LeaseID != "lease-native-1" ||
		envelope.Data.RuntimeServerID != "server-native-1" || envelope.Data.OperationID != "operation-native-1" ||
		envelope.Data.ResourceGenerationID == "" || envelope.Data.ProviderID != "ionos" ||
		envelope.Data.RuntimePhase != string(jobruntime.RuntimePhaseLeasePending) || envelope.Data.EnrollmentStatus != "pending" {
		t.Fatalf("response = %+v, want exact native pending correlations", envelope.Data)
	}
	if len(managedLeases.requests) != 1 {
		t.Fatalf("native admission calls = %d, want 1", len(managedLeases.requests))
	}
	request := managedLeases.requests[0]
	if request.OperationKey != envelope.Data.JobID || request.Provider != "ionos" || request.NodeRole != "storage" ||
		request.TenantID != "tenant-1" || request.OwnerID != "owner-1" || request.StackID != "stack-native-add" ||
		len(request.Services) != 2 || request.Services[0] != "files" || request.Services[1] != "pocket_id" {
		t.Fatalf("native admission request = %+v, want exact job-scoped intent", request)
	}
	legacy, err := legacyLeases.ListByTenant(context.Background(), "tenant-1")
	if err != nil || len(legacy) != 0 {
		t.Fatalf("legacy leases = %#v err=%v, want no legacy write", legacy, err)
	}
	stack, err := store.GetStack(context.Background(), "tenant-1", "stack-native-add")
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	if stack.RuntimeSummary["lease_id"] != nil {
		t.Fatalf("runtime summary = %#v, added server must not become stack primary", stack.RuntimeSummary)
	}
	job, err := store.GetJob(context.Background(), "tenant-1", envelope.Data.JobID)
	if err != nil || job.State != "completed" || job.Result["operation_id"] != "operation-native-1" {
		t.Fatalf("job = %+v err=%v, want completed native correlations", job, err)
	}
}

type stackLifecycleFakeRuntime struct {
	requests []serverruntime.LeaseRuntimeActionRequest
	onAction func(serverruntime.LeaseRuntimeActionRequest) error
}

type countingCreateLeaseService struct {
	stackLifecycleLeaseService
	createCalls int
}

func (s *countingCreateLeaseService) CreateOrUpdate(ctx context.Context, req vmleases.CreateRequest) (*vmlease.Lease, error) {
	s.createCalls++
	writer, ok := s.stackLifecycleLeaseService.(interface {
		CreateOrUpdate(context.Context, vmleases.CreateRequest) (*vmlease.Lease, error)
	})
	if !ok {
		return nil, errors.New("test lease writer is unavailable")
	}
	return writer.CreateOrUpdate(ctx, req)
}

func TestAddManagedRuntimeServerDoesNotCallProviderWhenStackExecutionBusy(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(context.Background(), controlplane.CreateStackRequest{
		ID:             "stack-busy-managed",
		TenantID:       "tenant-1",
		OwnerSubjectID: "owner-1",
		Name:           "Busy Managed",
		Status:         "running",
		Config: map[string]any{
			"server_provisioning_mode": "kombify-cloud",
			"runtime_lane":             "monthly-runtime",
			"lease_provider":           "ionos-managed",
		},
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	if _, err := store.CreateJob(context.Background(), controlplane.UpsertJobRequest{
		ID:       "job-running-deploy",
		TenantID: "tenant-1",
		StackID:  "stack-busy-managed",
		Type:     "deploy",
		State:    "pending",
	}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := store.StartJob(context.Background(), "tenant-1", "job-running-deploy", time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("StartJob: %v", err)
	}

	leaseService := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{
		Now:            func() time.Time { return time.Date(2026, 7, 19, 10, 1, 0, 0, time.UTC) },
		SnapshotSecret: []byte("secret"),
	})
	countingLeases := &countingCreateLeaseService{stackLifecycleLeaseService: leaseService}
	runtimeClient := &stackLifecycleFakeRuntime{}
	managedLeases := &stackLifecycleManagedLeaseManager{}
	event, recorder := stackLifecycleRouteTestEvent(http.MethodPost,
		"/api/v1/stacks/stack-busy-managed/managed-runtimes",
		`{"node_role":"worker","provider_id":"ionos"}`,
		"owner-1", "tenant-1")
	event.Request.SetPathValue("id", "stack-busy-managed")
	if err := (stackLifecycleRouteHandlers{
		leases:        countingLeases,
		runtime:       runtimeClient,
		features:      stackLifecycleFeatureChecker{enabled: true},
		stacks:        store,
		jobs:          store,
		managedLeases: managedLeases,
	}).addManagedRuntimeServer(event); err != nil {
		t.Fatalf("addManagedRuntimeServer returned router error: %v", err)
	}
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s, want 409", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode busy response: %v", err)
	}
	if envelope.Error.Code != "CONFLICT" || envelope.Error.Details["reason_code"] != "stack_execution_busy" || envelope.Error.Details["retryable"] != true {
		t.Fatalf("busy response = %#v", envelope)
	}
	rejectedJobID, _ := envelope.Error.Details["job_id"].(string)
	if rejectedJobID == "" {
		t.Fatalf("busy response missing rejected job id: %#v", envelope.Error.Details)
	}
	if countingLeases.createCalls != 0 {
		t.Fatalf("CreateOrUpdate calls = %d, want 0 while stack is busy", countingLeases.createCalls)
	}
	if len(managedLeases.requests) != 0 {
		t.Fatalf("native admission calls = %d, want 0 while stack is busy", len(managedLeases.requests))
	}
	if len(runtimeClient.requests) != 0 {
		t.Fatalf("runtime provider calls = %d, want 0 while stack is busy", len(runtimeClient.requests))
	}
	leases, err := leaseService.ListByTenant(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("ListByTenant: %v", err)
	}
	if len(leases) != 0 {
		t.Fatalf("leases = %d, want none while stack is busy", len(leases))
	}
	rejected, err := store.GetJob(context.Background(), "tenant-1", rejectedJobID)
	if err != nil {
		t.Fatalf("GetJob(rejected): %v", err)
	}
	if rejected.State != "pending" || rejected.StartedAt != nil {
		t.Fatalf("rejected job = %+v, want safely resumable unstarted job", rejected)
	}
	blocking, err := store.GetJob(context.Background(), "tenant-1", "job-running-deploy")
	if err != nil {
		t.Fatalf("GetJob(blocking): %v", err)
	}
	if blocking.State != "running" {
		t.Fatalf("blocking job state = %q, want running", blocking.State)
	}
}

func (f *stackLifecycleFakeRuntime) RuntimeAction(_ context.Context, req serverruntime.LeaseRuntimeActionRequest) (*serverruntime.LeaseRuntimeActionResponse, error) {
	f.requests = append(f.requests, req)
	if f.onAction != nil {
		if err := f.onAction(req); err != nil {
			return nil, err
		}
	}
	return &serverruntime.LeaseRuntimeActionResponse{
		TenantID:   req.TenantID,
		LeaseID:    req.LeaseID,
		Action:     req.Action,
		OfferingID: req.OfferingID,
	}, nil
}

func TestAddManagedRuntimeServerReturnsStructuredErrorOnProvisionFailure(t *testing.T) {
	store := controlplane.NewMemoryStore()
	stack, err := store.CreateStack(context.Background(), controlplane.CreateStackRequest{
		ID: "stack-failure", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Status: "pending",
		Config: map[string]any{"server_provisioning_mode": "kombify-cloud", "server_mode": "monthly-runtime", "runtime_lane": "monthly-runtime", "runtime_offering_id": "monthly-runtime-standard", "lease_provider": "centron-managed"},
	})
	if err != nil {
		t.Fatalf("seed stack: %v", err)
	}
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{
		Now:            func() time.Time { return time.Date(2026, 5, 26, 8, 0, 0, 0, time.UTC) },
		SnapshotSecret: []byte("secret"),
	})
	legacyWriter := &countingCreateLeaseService{stackLifecycleLeaseService: leases}
	managedLeases := &stackLifecycleManagedLeaseManager{err: errors.New("native admission failed")}

	body := `{"node_role":"storage","provider_id":"ionos","runtime_offering_id":"monthly-runtime-premium","stackkit":"basement-kit","services":["files"]}`
	event, recorder := stackLifecycleRouteTestEvent(http.MethodPost, "/api/v1/stacks/"+stack.ID+"/managed-runtimes", body, "owner-1", "tenant-1")
	event.Request.SetPathValue("id", stack.ID)
	if err = (stackLifecycleRouteHandlers{
		leases: legacyWriter, managedLeases: managedLeases, stacks: store,
		features: stackLifecycleFeatureChecker{enabled: true}, jobs: store,
	}).addManagedRuntimeServer(event); err != nil {
		t.Fatalf("addManagedRuntimeServer returned router error: %v", err)
	}
	// An admission error can be a lost commit response. It must remain safely
	// resumable under the same key instead of being terminalized.
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s, want 503", recorder.Code, recorder.Body.String())
	}
	var env struct {
		Error struct {
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err = json.Unmarshal(recorder.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error body %q: %v", recorder.Body.String(), err)
	}
	d := env.Error.Details
	if d["reason_code"] != "native_admission_outcome_unconfirmed" {
		t.Errorf("reason_code = %v, want native_admission_outcome_unconfirmed", d["reason_code"])
	}
	if d["retryable"] != true {
		t.Errorf("retryable = %v, want true", d["retryable"])
	}
	jobID, _ := d["job_id"].(string)
	if jobID == "" {
		t.Fatal("job_id missing from provision-failure error details")
	}
	job, err := store.GetJob(context.Background(), "tenant-1", jobID)
	if err != nil {
		t.Fatalf("find add-server job: %v", err)
	}
	if job.State != "running" || job.StartedAt == nil {
		t.Fatalf("job = %+v, want claimed and recoverable", job)
	}
	if legacyWriter.createCalls != 0 || len(managedLeases.requests) != 1 {
		t.Fatalf("legacy/native calls = %d/%d, want 0/1", legacyWriter.createCalls, len(managedLeases.requests))
	}
}

func stackLifecycleRouteTestEvent(method, target, body, ownerID, orgID string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set(managedRuntimeIdempotencyHeader, "test-add-server-request")
	if ownerID != "" {
		req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{UserID: ownerID, OrgID: orgID}))
	}
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}
