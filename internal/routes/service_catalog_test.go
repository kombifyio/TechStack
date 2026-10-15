package routes

import (
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

func localRuntimeTarget(observedAt time.Time) serverregistry.RuntimeTarget {
	return serverregistry.RuntimeTarget{
		EnvironmentClass: serverregistry.EnvironmentLocal, Offering: serverregistry.OfferingSelfOwnedDevice,
		AvailabilityOwner: serverregistry.AvailabilityCustomer, OperationsOwner: serverregistry.OperationsCustomer,
		EvidenceRef: "owner-pairing:worker-1", ObservedAt: &observedAt,
	}
}

// Rows persisted before recognition existed carry application_key "system" for
// every observed unit. They must resurface as their own applications on read,
// while infrastructure and unrecognized systemd units stay in System.
func TestPersistedObservedRowsReclassifyIntoOwnApplications(t *testing.T) {
	store := controlplane.NewMemoryStore()
	ctx := t.Context()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Demo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServerRuntime(ctx, controlplane.ServerRuntime{ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", ConnectionState: "connected", HealthState: "healthy", RuntimeTarget: localRuntimeTarget(now)}); err != nil {
		t.Fatal(err)
	}
	observed := func(id, name string, metadata map[string]any) controlplane.ServiceRuntime {
		metadata["application_key"], metadata["application_display_name"] = "system", "System Services"
		return controlplane.ServiceRuntime{ID: id, TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1", ServiceKey: id, Name: name, ManagementState: "observed", ObservedState: "running", HealthState: "healthy", ObservedAt: &now, Metadata: metadata}
	}
	for _, row := range []controlplane.ServiceRuntime{
		observed("svc-hermes", "hermes-webui", map[string]any{"platform_type": "docker", "image": "ghcr.io/nesquena/hermes-webui:v0.4.1"}),
		observed("svc-notes", "my-notes", map[string]any{"platform_type": "docker", "image": "someone/notes:latest"}),
		observed("svc-proxy", "traefik", map[string]any{"platform_type": "docker", "image": "traefik:v3.1"}),
		observed("svc-backup", "custom-backup.service", map[string]any{"platform_type": "systemd", "runtime_identity": map[string]any{"kind": "systemd_unit", "unit": "custom-backup.service"}}),
	} {
		if _, err := store.UpsertServiceRuntime(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	h := serviceRuntimeHandlers{store: store, stacks: store, servers: store, now: func() time.Time { return now }}
	event, _ := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/service-applications?kit_deployment_id=stack-1", "owner-1", "tenant-1", nil)
	applications, err := h.applicationReadModel(event)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]serviceApplicationResponse{}
	for _, application := range applications {
		byKey[application.ApplicationKey] = application
	}

	hermes, ok := byKey["hermes"]
	if !ok || hermes.System || hermes.DisplayName != "Hermes Agent" {
		t.Fatalf("hermes-webui must be its own recognized application: %#v", applications)
	}
	if hermes.Catalog == nil || hermes.Catalog.ID != "hermes" || hermes.Version == nil || hermes.Version.Current != "0.4.1" || hermes.Version.Source != "image_tag" {
		t.Fatalf("hermes catalog/version = %#v %#v", hermes.Catalog, hermes.Version)
	}
	if hermes.PlacementClass != "local" {
		t.Fatalf("placement_class = %q, want the recorded local target", hermes.PlacementClass)
	}
	notes, ok := byKey["my-notes"]
	if !ok || notes.System || (notes.Catalog != nil && notes.Catalog.Category != "unknown") || notes.Version == nil || notes.Version.Current != "" || notes.Version.ReasonCode != "floating_tag" {
		t.Fatalf("an unrecognized container must be its own application with an honest floating-tag version: %#v", notes)
	}
	system, ok := byKey["system"]
	if !ok || !system.System || system.DisplayName != "System Services" {
		t.Fatalf("infra and bare units must stay in System: %#v", applications)
	}
	var members []string
	for _, component := range system.Components {
		members = append(members, component.Name)
	}
	if !slices.Contains(members, "traefik") || !slices.Contains(members, "custom-backup.service") || slices.Contains(members, "hermes-webui") {
		t.Fatalf("system members = %v", members)
	}
}

// Kit and compiler versions belong to separate namespaces. A managed component
// must expose its measured image without granting a guessed compiler upgrade.
func TestServiceUpdateRefusesMixedKitAndCompilerVersions(t *testing.T) {
	store := controlplane.NewMemoryStore()
	ctx := t.Context()
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	observedAt := now.Add(-10 * time.Second)
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackKitInstanceID: "family-main", Name: "Stack", Config: map[string]any{"stackkit": "family-lab"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServerRuntime(ctx, controlplane.ServerRuntime{ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", WorkerID: "agent-1", InventoryRevision: 7, LifecycleState: string(serverregistry.LifecycleActive), ConnectionState: string(serverregistry.ConnectionConnected), LastHeartbeatAt: &observedAt, Metadata: map[string]any{"stackkit": "family-lab"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServiceRuntime(ctx, controlplane.ServiceRuntime{ID: "service-1", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1", ServiceKey: "auth", ManagementState: "managed", StackKitVersion: "family-lab@v0.1.0", ObservedAt: &observedAt, Capabilities: []string{"stop"}, Metadata: map[string]any{"inventory_revision": int64(7), "image": "example/component:v3", "image_version": "3.6.1"}}); err != nil {
		t.Fatal(err)
	}
	orch := &recordingServiceActionOrchestrator{store: store}
	pinned := "v0.2.0"
	h := serviceRuntimeHandlers{store: store, stacks: store, servers: store, jobs: store, now: func() time.Time { return now }, orch: orch, stackKitRelease: func() string { return pinned }}
	postUpdate := func(key string) int {
		event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/registry/services/service-1/actions", "owner-1", "tenant-1", map[string]any{"action": "update", "expected_inventory_revision": 7, "owner_approved": true})
		event.Request.SetPathValue("serviceId", "service-1")
		event.Request.Header.Set("Idempotency-Key", key)
		if err := h.action(event); err != nil && !errors.Is(err, httpx.ErrResponseWritten) {
			t.Fatal(err)
		}
		return recorder.Code
	}
	advertised := func() serviceRuntimeResponse {
		row, err := store.GetServiceRuntime(ctx, "tenant-1", "service-1")
		if err != nil {
			t.Fatal(err)
		}
		server, err := store.GetServerRuntime(ctx, "tenant-1", "server-1")
		if err != nil {
			t.Fatal(err)
		}
		return h.response(*row, server)
	}

	behind := advertised()
	if behind.Version == nil || behind.Version.Current != "3.6.1" || behind.Version.Source != "image_label" || behind.Version.UpdateAvailable || behind.Version.Latest != "" || behind.Version.CheckedAt != nil || behind.Version.ReasonCode != reasonCompilerReleaseUnknown || slices.Contains(behind.AllowedActions, "update") {
		t.Fatalf("component image or unavailable compiler provenance was misrepresented: %#v", behind)
	}
	if code := postUpdate("update-1"); code != http.StatusConflict || len(orch.requests) != 0 {
		t.Fatalf("update status=%d dispatched=%d", code, len(orch.requests))
	}

	pinned = "v0.1.0"
	current := advertised()
	if current.Version == nil || current.Version.UpdateAvailable || slices.Contains(current.AllowedActions, "update") {
		t.Fatalf("a stack on the pin must not advertise update: %#v", current)
	}
	if code := postUpdate("update-2"); code != http.StatusConflict || len(orch.requests) != 0 {
		t.Fatalf("update on a current stack status=%d dispatched=%d", code, len(orch.requests))
	}
}
