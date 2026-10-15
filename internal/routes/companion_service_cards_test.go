package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

// A service id is not permission to query another owner's container telemetry.
func TestCompanionServiceMetricsRejectsAnotherOwnerBeforeQuery(t *testing.T) {
	store, now := lockableServiceFixture(t)
	backend := &forbiddenServiceMetricsBackend{t: t}
	h := serviceRuntimeHandlers{store: store, stacks: store, servers: store, metrics: backend, now: func() time.Time { return now }}
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/services/service-1/metrics", "other-owner", "tenant-1", nil)
	event.Request.SetPathValue("serviceId", "service-1")
	if err := h.serviceMetrics(event); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("foreign telemetry authorization returned %d: %s", recorder.Code, recorder.Body.String())
	}
}

// Job-output cards addressed through worker/hostname aliases must open the same
// server-bound record listed on a multi-server deployment.
func TestCompanionOutputAliasCardOpensItsListedPlacement(t *testing.T) {
	store := controlplane.NewMemoryStore()
	mustCreateJobStoreStack(t, store, "tenant-1", "stack-1", "owner-1")
	for _, suffix := range []string{"one", "two"} {
		if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{ID: "server-" + suffix, TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", WorkerID: "worker-" + suffix, Name: "display-" + suffix}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.UpsertNode(t.Context(), controlplane.Node{ID: "old-node-" + suffix, TenantID: "tenant-1", StackID: "stack-1", WorkerID: "worker-" + suffix, Name: "hostname-" + suffix}); err != nil {
			t.Fatal(err)
		}
	}
	jobs := outputAliasJobReader{JobStore: store}
	list := registryRouteHandlers{stackStore: store, registryStore: store, serverStore: store, serviceStore: store, jobStore: jobs}
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/registry/services", "owner-1", "tenant-1", nil)
	if err := list.services(event); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data registryPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	read := serviceRuntimeHandlers{store: store, stacks: store, servers: store, jobs: jobs, now: time.Now}
	opened := map[string]bool{}
	for _, card := range envelope.Data.Services {
		detail, response := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/services/"+card.ID, "owner-1", "tenant-1", nil)
		detail.Request.SetPathValue("serviceId", card.ID)
		if err := read.get(detail); err != nil {
			t.Fatal(err)
		}
		var got struct {
			Data serviceRuntimeResponse `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || got.Data.ServerID != card.ServerID {
			t.Fatalf("listed alias card failed to open its placement: %#v %s", card, response.Body.String())
		}
		opened[got.Data.ServerID] = true
	}
	if !opened["server-one"] || !opened["server-two"] {
		t.Fatalf("alias placements were not independently readable: %#v", opened)
	}
}

type outputAliasJobReader struct{ controlplane.JobStore }

func (s outputAliasJobReader) ListJobsByStack(context.Context, string, string, int) ([]controlplane.Job, error) {
	return []controlplane.Job{{Type: "deploy", State: "completed", Result: map[string]any{"stackkit_outputs": map[string]any{"services": []any{
		map[string]any{"name": "sample", "target_server": "worker-one"},
		map[string]any{"name": "sample", "target_server": "hostname-two"},
	}}}}}, nil
}

type forbiddenServiceMetricsBackend struct {
	monitoring.MetricsQueryBackend
	t *testing.T
}

func (b *forbiddenServiceMetricsBackend) InstantQuery(context.Context, string, time.Time) (*monitoring.QueryResult, error) {
	b.t.Fatal("foreign service queried the telemetry backend")
	return nil, nil
}

// Regression: the list used to bypass the runtime owner lock and expose a
// different placement/action set from the detail the card subsequently opened.
func TestCompanionRegistryCardPreservesRuntimeLockAndPlacement(t *testing.T) {
	store, now := lockableServiceFixture(t)
	if _, err := store.SetServiceMutationLock(t.Context(), "tenant-1", "service-1", controlplane.ServiceMutationLock{State: serviceregistry.MutationLocked, ReasonCode: "owner_locked", Actor: "owner-1"}, "owner_locked"); err != nil {
		t.Fatal(err)
	}
	h := registryRouteHandlers{stackStore: store, workerStore: store, registryStore: store, serverStore: store, serviceStore: store, serviceProjector: &serviceRuntimeHandlers{now: func() time.Time { return now }}}
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/registry/services", "owner-1", "tenant-1", nil)
	if err := h.services(event); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Data registryPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var card registryService
	for _, row := range list.Data.Services {
		if row.ID == "service-1" {
			card = row
		}
	}
	if card.ServerID != "server-1" || card.InventoryRevision != 7 || card.MutationLock.State != "locked" || containsString(card.AllowedActions, "restart") || !containsString(card.AllowedActions, "logs") {
		t.Fatalf("registry card bypasses runtime state: %#v", card)
	}
	read := serviceRuntimeHandlers{store: store, stacks: store, servers: store, now: func() time.Time { return now }}
	detailEvent, detailRecorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/services/service-1", "owner-1", "tenant-1", nil)
	detailEvent.Request.SetPathValue("serviceId", card.ID)
	if err := read.get(detailEvent); err != nil {
		t.Fatal(err)
	}
	var detail struct {
		Data serviceRuntimeResponse `json:"data"`
	}
	if err := json.Unmarshal(detailRecorder.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Data.ServerID != card.ServerID || detail.Data.MutationLock.State != card.MutationLock.State || containsString(detail.Data.AllowedActions, "restart") {
		t.Fatalf("detail and card disagree on runtime control: %#v %#v", card, detail.Data)
	}
}

// Regression: an unplaced legacy row used to disappear from the detail route.
// Keeping it readable must never turn its historical status into control rights.
func TestCompanionLegacyCardCanBeReadWithoutInventingControl(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertService(t.Context(), controlplane.Service{ID: "stack-1:traefik", TenantID: "tenant-1", StackID: "stack-1", ServiceKey: "traefik", Name: "Traefik", Status: "running", Source: "stackkit_outputs"}); err != nil {
		t.Fatal(err)
	}
	h := serviceRuntimeHandlers{store: store, stacks: store, servers: store, jobs: store, now: time.Now}
	event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/services/stack-1:traefik", "owner-1", "tenant-1", nil)
	event.Request.SetPathValue("serviceId", "stack-1:traefik")
	if err := h.get(event); err != nil {
		t.Fatal(err)
	}
	var detail struct {
		Data serviceRuntimeResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || detail.Data.ObservedState != "unknown" || detail.Data.ServerID != "" || containsString(detail.Data.AllowedActions, "restart") || detail.Data.Catalog.IconURL == "" {
		t.Fatalf("unplaced legacy service fabricated runtime facts: %s", recorder.Body.String())
	}
	logs, logsRecorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/registry/services/stack-1:traefik/logs", "owner-1", "tenant-1", nil)
	logs.Request.SetPathValue("serviceId", "stack-1:traefik")
	if err := h.logs(logs); err != nil {
		t.Fatal(err)
	}
	if logsRecorder.Code != http.StatusOK {
		t.Fatalf("legacy logs unreadable: %s", logsRecorder.Body.String())
	}
}

// Service activity must not contain other service operations from the same
// deployment, or a card can present somebody else's operation as its own.
func TestCompanionServiceJobFilterKeepsRecordedServiceTarget(t *testing.T) {
	store := controlplane.NewMemoryStore()
	mustCreateJobStoreStack(t, store, "tenant-1", "stack-1", "owner-1")
	for _, target := range []string{"service-selected", "service-other"} {
		if _, err := store.CreateJob(t.Context(), controlplane.UpsertJobRequest{ID: "job-" + target, TenantID: "tenant-1", StackID: "stack-1", Type: "service_restart", State: "pending", Result: map[string]any{"service_action_receipt": map[string]any{"service_id": target}}}); err != nil {
			t.Fatal(err)
		}
	}
	router := httpx.NewRouter()
	RegisterJobsSSERoutes(router, JobRouteStores{Stacks: store, Jobs: store})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs?service_id=service-selected", nil)
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	var response struct {
		Data jobListPayload `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("activity read failed: %s", recorder.Body.String())
	}
	selected := false
	for _, job := range response.Data.Items {
		if job["id"] != "job-service-selected" {
			t.Fatalf("other service operation exposed: %#v", job)
		}
		selected = true
	}
	if !selected {
		t.Fatalf("recorded operation missing from selected service activity: %s", recorder.Body.String())
	}
}
