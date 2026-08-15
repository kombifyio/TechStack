package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/stackrouting"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

type routingTestLeaseLister struct{ leases []vmlease.Lease }

var routingTestServerID = runtimeidentity.LeaseServerID("lease-1")

type routingTestDispatcher struct {
	jobID string
	calls int
}

func (d *routingTestDispatcher) DispatchRoutingRollout(context.Context, stackrouting.RolloutRequest) (*stackrouting.RolloutResult, error) {
	d.calls++
	return &stackrouting.RolloutResult{JobID: d.jobID}, nil
}

func (l routingTestLeaseLister) ListByTenant(context.Context, string) ([]vmlease.Lease, error) {
	return l.leases, nil
}

func (l routingTestLeaseLister) ListInventoryByTenant(context.Context, string) ([]vmleases.LeaseInventoryRecord, error) {
	records := make([]vmleases.LeaseInventoryRecord, 0, len(l.leases))
	for _, lease := range l.leases {
		state := vmleases.LeaseAuthorityStateNativeActive
		if !monthlyruntime.LeaseActive(lease) || !lease.ActiveAt(time.Now().UTC()) {
			state = vmleases.LeaseAuthorityStateNativeInactive
		}
		records = append(records, vmleases.LeaseInventoryRecord{
			Lease:              lease,
			ExecutionAuthority: vmleases.LeaseExecutionAuthorityTechStackProviderControl,
			AuthorityState:     state,
		})
	}
	return records, nil
}

func routingTestManagedLease(id, tenantID, ownerID, stackID string) vmlease.Lease {
	now := time.Now().UTC()
	return vmlease.Lease{
		ID: vmlease.LeaseID(id), Subject: vmlease.Subject{Kind: vmlease.SubjectUser, ID: ownerID, OrgID: tenantID},
		Resource: vmlease.ResourceRef{ProviderID: monthlyruntime.ProviderIONOS}, DesiredState: vmlease.DesiredStateRunning,
		BillingMode: vmlease.BillingModeSubscription, LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy: vmlease.RestartPolicyOnUnexpectedStop, RecreatePolicy: vmlease.RecreatePolicyManual,
		ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour), RenewedAt: now,
		Metadata: monthlyruntime.NormalizeMetadata(map[string]string{
			"stack_id": stackID, "lease_provider": monthlyruntime.ProviderIONOS,
		}, serverruntime.RuntimeOfferingStandard),
	}
}

func publicRoutingEvent(method, body, stackID, idempotencyKey, ifMatch string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, "/api/v1/stacks/"+stackID+"/routing", strings.NewReader(body))
	req.SetPathValue("id", stackID)
	req.Header.Set("content-type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: "auth0|user-1", OrgID: "tenant-1"}))
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

func TestPutAndGetStackRoutingUseSameDesiredStateContract(t *testing.T) {
	resources := controlplane.NewMemoryStore()
	routingStore := stackrouting.NewMemoryStore()
	seedStack(t, resources, "stack-1", "tenant-1", "auth0|user-1", "running")
	seedManagedRoutingServer(t, resources, routingTestServerID, "tenant-1", "auth0|user-1", "stack-1", "lease-1")
	dispatcher := &routingTestDispatcher{jobID: "job-routing-1"}
	h := crudRouteHandlers{stackStore: resources, serverStore: resources, routingStore: routingStore, jobStore: resources,
		routingLeases: routingTestLeaseLister{leases: []vmlease.Lease{routingTestManagedLease("lease-1", "tenant-1", "auth0|user-1", "stack-1")}}, routingDispatch: dispatcher}

	body := strings.Replace(`{"server_id":"server-1","lease_id":"lease-1","mode":"custom-domain","domain":"kombified.com","provenance":{"source":"operator","dns_provider":"cloudflare","zone_id":"zone-1"},"ensure_rollout":true}`, "server-1", routingTestServerID, 1)
	put, putRec := publicRoutingEvent(http.MethodPut, body, "stack-1", "routing-1", "")
	if err := h.putStackRouting(put); err != nil {
		t.Fatal(err)
	}
	if putRec.Code != http.StatusAccepted || putRec.Header().Get("ETag") != `"1"` {
		t.Fatalf("PUT status=%d etag=%q body=%s", putRec.Code, putRec.Header().Get("ETag"), putRec.Body.String())
	}
	jobs, err := resources.ListJobsByStack(context.Background(), "tenant-1", "stack-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("routing endpoint fabricated a rollout job: %#v", jobs)
	}
	if dispatcher.calls != 1 {
		t.Fatalf("dispatcher calls = %d, want one", dispatcher.calls)
	}

	get, getRec := publicRoutingEvent(http.MethodGet, "", "stack-1", "", "")
	if err := h.getStackRouting(get); err != nil {
		t.Fatal(err)
	}
	if getRec.Code != http.StatusOK || getRec.Header().Get("ETag") != `"1"` {
		t.Fatalf("GET status=%d etag=%q body=%s", getRec.Code, getRec.Header().Get("ETag"), getRec.Body.String())
	}
	var envelope ksapi.SuccessResponse
	if err := json.Unmarshal(getRec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	data, _ := envelope.Data.(map[string]any)
	desired, _ := data["desired"].(map[string]any)
	observed, _ := data["observed"].(map[string]any)
	if desired["domain"] != "kombified.com" || observed["applied"] != false {
		t.Fatalf("GET data = %#v", data)
	}
}

func TestPutStackRoutingHonorsIfMatch(t *testing.T) {
	resources := controlplane.NewMemoryStore()
	routingStore := stackrouting.NewMemoryStore()
	seedStack(t, resources, "stack-1", "tenant-1", "auth0|user-1", "running")
	seedManagedRoutingServer(t, resources, routingTestServerID, "tenant-1", "auth0|user-1", "stack-1", "lease-1")
	h := crudRouteHandlers{stackStore: resources, serverStore: resources, routingStore: routingStore,
		routingLeases: routingTestLeaseLister{leases: []vmlease.Lease{routingTestManagedLease("lease-1", "tenant-1", "auth0|user-1", "stack-1")}}, routingDispatch: &routingTestDispatcher{jobID: "job-routing-2"}}
	body := strings.Replace(`{"server_id":"server-1","lease_id":"lease-1","domain":"kombified.com","provenance":{"source":"operator","dns_provider":"manual"}}`, "server-1", routingTestServerID, 1)
	first, _ := publicRoutingEvent(http.MethodPut, body, "stack-1", "routing-1", "")
	_ = h.putStackRouting(first)
	changed := strings.Replace(body, "kombified.com", "other.example", 1)
	second, rec := publicRoutingEvent(http.MethodPut, changed, "stack-1", "routing-2", `"0"`)
	_ = h.putStackRouting(second)
	if rec.Code != http.StatusPreconditionFailed || !strings.Contains(rec.Body.String(), "routing_revision_conflict") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPutStackRoutingReportsRolloutInProgressAsConflict(t *testing.T) {
	resources := controlplane.NewMemoryStore()
	routingStore := stackrouting.NewMemoryStore()
	seedStack(t, resources, "stack-1", "tenant-1", "auth0|user-1", "running")
	seedManagedRoutingServer(t, resources, routingTestServerID, "tenant-1", "auth0|user-1", "stack-1", "lease-1")
	h := crudRouteHandlers{stackStore: resources, serverStore: resources, routingStore: routingStore,
		routingLeases: routingTestLeaseLister{leases: []vmlease.Lease{routingTestManagedLease("lease-1", "tenant-1", "auth0|user-1", "stack-1")}}, routingDispatch: &routingTestDispatcher{jobID: "job-routing-pending"}}
	body := strings.Replace(`{"server_id":"server-1","lease_id":"lease-1","domain":"kombified.com","provenance":{"source":"operator","dns_provider":"manual"},"ensure_rollout":true}`, "server-1", routingTestServerID, 1)
	first, _ := publicRoutingEvent(http.MethodPut, body, "stack-1", "routing-1", "")
	_ = h.putStackRouting(first)
	changed := strings.Replace(body, "kombified.com", "other.example", 1)
	second, rec := publicRoutingEvent(http.MethodPut, changed, "stack-1", "routing-2", "")
	_ = h.putStackRouting(second)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "routing_rollout_in_progress") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPutStackRoutingAllowsLocalServerWithoutLease(t *testing.T) {
	resources := controlplane.NewMemoryStore()
	routingStore := stackrouting.NewMemoryStore()
	seedStack(t, resources, "stack-local", "tenant-1", "auth0|user-1", "running")
	if _, err := resources.UpsertServerRuntime(context.Background(), controlplane.ServerRuntime{
		ID: "server-local", TenantID: "tenant-1", StackID: "stack-local", OwnerSubjectID: "auth0|user-1", Name: "Basement",
		ProviderRef: "local",
	}); err != nil {
		t.Fatal(err)
	}
	body := `{"server_id":"server-local","domain":"home.example","provenance":{"source":"operator","dns_provider":"manual"}}`
	e, rec := publicRoutingEvent(http.MethodPut, body, "stack-local", "routing-local", "")
	_ = (crudRouteHandlers{stackStore: resources, serverStore: resources, routingStore: routingStore}).putStackRouting(e)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	state, err := routingStore.Get(context.Background(), "tenant-1", "stack-local")
	if err != nil || state.LeaseID != "" {
		t.Fatalf("state=%#v err=%v", state, err)
	}
}

func TestListStackRoutingTargetsIsReadOnlyAndExcludesWorkerLeases(t *testing.T) {
	resources := controlplane.NewMemoryStore()
	seedStack(t, resources, "stack-1", "tenant-1", "auth0|user-1", "running")
	foundation := routingTestManagedLease("lease-foundation", "tenant-1", "auth0|user-1", "stack-1")
	foundation.Metadata["role"] = "foundation"
	foundation.Metadata["runtime_public_ip"] = "203.0.113.8"
	worker := routingTestManagedLease("lease-worker", "tenant-1", "auth0|user-1", "stack-1")
	worker.Metadata["role"] = "worker"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/stacks/stack-1/routing/targets", nil)
	req.SetPathValue("id", "stack-1")
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: "auth0|user-1", OrgID: "tenant-1"}))
	rec := httptest.NewRecorder()
	e := &httpx.Event{Request: req, Response: rec}
	h := crudRouteHandlers{stackStore: resources, serverStore: resources, routingLeases: routingTestLeaseLister{leases: []vmlease.Lease{worker, foundation}}}
	if err := h.listStackRoutingTargets(e); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data struct {
			Targets []stackrouting.Target `json:"targets"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Targets) != 1 || envelope.Data.Targets[0].LeaseID != "lease-foundation" || envelope.Data.Targets[0].ServerID != runtimeidentity.LeaseServerID("lease-foundation") || envelope.Data.Targets[0].Address != "203.0.113.8" {
		t.Fatalf("targets = %#v", envelope.Data.Targets)
	}
	if _, err := resources.GetServerRuntime(context.Background(), "tenant-1", runtimeidentity.LeaseServerID("lease-foundation")); !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatalf("read-only target discovery created a server projection: %v", err)
	}
}

func TestPutStackRoutingRequiresIdempotencyKeyAndExactLease(t *testing.T) {
	resources := controlplane.NewMemoryStore()
	seedStack(t, resources, "stack-1", "tenant-1", "auth0|user-1", "running")
	seedManagedRoutingServer(t, resources, routingTestServerID, "tenant-1", "auth0|user-1", "stack-1", "lease-1")
	lease := routingTestManagedLease("lease-1", "tenant-1", "auth0|user-1", "stack-1")
	h := crudRouteHandlers{stackStore: resources, serverStore: resources, routingStore: stackrouting.NewMemoryStore(), routingLeases: routingTestLeaseLister{leases: []vmlease.Lease{lease}}}
	body := strings.Replace(`{"server_id":"server-1","lease_id":"lease-1","domain":"kombified.com","provenance":{"source":"operator","dns_provider":"manual"}}`, "server-1", routingTestServerID, 1)

	missing, missingRecorder := publicRoutingEvent(http.MethodPut, body, "stack-1", "", "")
	_ = h.putStackRouting(missing)
	if missingRecorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing key status=%d body=%s", missingRecorder.Code, missingRecorder.Body.String())
	}

	wrongBody := strings.Replace(body, "lease-1", "lease-other", 1)
	wrong, wrongRecorder := publicRoutingEvent(http.MethodPut, wrongBody, "stack-1", "wrong-lease-key", "")
	_ = h.putStackRouting(wrong)
	if wrongRecorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong lease status=%d body=%s", wrongRecorder.Code, wrongRecorder.Body.String())
	}
}
