package routes

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

func TestDeclaredComponentUsesPublishedControlAndRespectsGroupFreeze(t *testing.T) {
	store, now := lockableServiceFixture(t)
	control, err := store.GetServiceRuntime(t.Context(), "tenant-1", "service-1")
	if err != nil {
		t.Fatal(err)
	}
	control.Source = serviceregistry.SourceStackKitsInventory
	control.ManagementState = string(serviceregistry.ManagementManaged)
	if _, err := store.UpsertServiceRuntime(t.Context(), *control); err != nil {
		t.Fatal(err)
	}
	server, err := store.GetServerRuntime(t.Context(), "tenant-1", control.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	server.LifecycleState = string(serverregistry.LifecycleActive)
	server.Metadata = map[string]any{"stackkit": "family-lab"}
	if _, err := store.UpsertServerRuntime(t.Context(), *server); err != nil {
		t.Fatal(err)
	}
	component := *control
	component.ID, component.ServiceKey, component.Name = "component", "api", "Application API"
	component.Capabilities = []string{}
	component.Metadata = map[string]any{"inventory_revision": int64(7), "application_key": control.ServiceKey, "runtime_identity": map[string]any{"container_name": "application-api"}}
	if _, err := store.UpsertServiceRuntime(t.Context(), component); err != nil {
		t.Fatal(err)
	}
	orch := &recordingServiceActionOrchestrator{store: store}
	h := serviceRuntimeHandlers{store: store, stacks: store, servers: store, jobs: store, now: func() time.Time { return now }, orch: orch, stackKitRelease: func() string { return "v0.2.0" }}
	read := func(id string) serviceRuntimeResponse {
		event, response := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/services/"+id, "owner-1", "tenant-1", nil)
		event.Request.SetPathValue("serviceId", id)
		if err := h.get(event); err != nil {
			t.Fatal(err)
		}
		var detail struct {
			Data serviceRuntimeResponse `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		return detail.Data
	}
	before := read(component.ID)
	if !slices.Contains(before.AllowedActions, serviceActionRestart) || before.Provenance["control_scope"] != "application" || before.Provenance[serviceControlID] != control.ID {
		t.Fatalf("component control scope was lost: %#v", before)
	}
	post := func(id, action string, revision int64) int {
		event, response := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/registry/services/"+id+"/actions", "owner-1", "tenant-1", map[string]any{"action": action, "expected_inventory_revision": revision, "owner_approved": true})
		event.Request.SetPathValue("serviceId", id)
		event.Request.Header.Set("Idempotency-Key", id+"-"+action)
		if err := h.action(event); err != nil && !errors.Is(err, httpx.ErrResponseWritten) {
			t.Fatal(err)
		}
		return response.Code
	}
	if status := post(component.ID, "restart", 7); status != http.StatusAccepted {
		t.Fatalf("published component control refused: %d", status)
	}
	if len(orch.requests) == 0 {
		t.Fatal("component command did not reach the existing executor")
	}
	dispatched := orch.requests[0]
	if dispatched.ServiceID != component.ID || dispatched.ServiceKey != control.ServiceKey {
		t.Fatalf("command changed component identity or missed its declared control: %#v", dispatched)
	}
	if status := post(component.ID, "freeze", 0); status != http.StatusOK {
		t.Fatalf("freeze failed: %d", status)
	}
	if status := post(control.ID, "restart", 7); status != http.StatusConflict {
		t.Fatalf("control bypassed its frozen component: %d", status)
	}
	if status := post(control.ID, serviceActionUpdate, 7); status != http.StatusConflict {
		t.Fatalf("update bypassed its frozen component: %d", status)
	}
	after := read(control.ID)
	if slices.Contains(after.AllowedActions, serviceActionUpdate) || slices.Contains(after.AllowedActions, serviceActionUnfreeze) || after.MutationLock.State != "unlocked" || after.ActionGuard["reason_code"] != "application_control_locked" {
		t.Fatalf("group lock advertisement lost the actual lock ownership: %#v", after)
	}
}

func TestSupersededObservationLeavesActiveCardsButRetainsHistory(t *testing.T) {
	store, now := lockableServiceFixture(t)
	current, err := store.GetServiceRuntime(t.Context(), "tenant-1", "service-1")
	if err != nil {
		t.Fatal(err)
	}
	current.Name = "Application"
	current.Source = serviceregistry.SourceStackKitsInventory
	current.Metadata["runtime_identity"] = map[string]any{"kind": "docker_compose_service", "project": "application", "service": "api", "file": ".stackkit/runtime/app/compose.yaml", "container_name": "application-api"}
	if _, err := store.UpsertServiceRuntime(t.Context(), *current); err != nil {
		t.Fatal(err)
	}
	history := *current
	history.ID, history.ServiceKey, history.Name = "historical-observation", "docker/application_api", "application-api"
	history.Source, history.ManagementState = serviceregistry.SourceObserved, string(serviceregistry.ManagementObserved)
	history.Access = map[string]any{"reason_code": "discovery_service_absent"}
	history.Capabilities = []string{}
	history.Metadata = map[string]any{"inventory_revision": int64(7), "runtime_identity": map[string]any{"kind": "docker_compose_service", "project": "application", "service": "api", "file": "/opt/app/.stackkit/runtime/app/compose.yaml"}}
	if _, err := store.UpsertServiceRuntime(t.Context(), history); err != nil {
		t.Fatal(err)
	}
	h := serviceRuntimeHandlers{store: store, stacks: store, servers: store, jobs: store, now: func() time.Time { return now }}
	event, response := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/services", "owner-1", "tenant-1", nil)
	if err := h.list(event); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Data []serviceRuntimeResponse `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	active := false
	for _, card := range list.Data {
		if card.ID == history.ID {
			t.Fatal("superseded discovery remained an active card")
		}
		active = active || card.ID == current.ID
	}
	if !active {
		t.Fatal("measured application was lost during reconciliation")
	}
	event, response = registryRouteStoreTestEvent(http.MethodGet, "/api/v1/services/"+history.ID, "owner-1", "tenant-1", nil)
	event.Request.SetPathValue("serviceId", history.ID)
	if err := h.get(event); err != nil {
		t.Fatal(err)
	}
	var detail struct {
		Data serviceRuntimeResponse `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || detail.Data.ID != history.ID || detail.Data.Provenance[serviceSupersededBy] != current.ID {
		t.Fatalf("historical identity became unreadable or lost its measured counterpart: %#v", detail.Data)
	}
}
