package routes

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/controlplane"
)

// Regression: the Registry services read evaluated the signed application
// adoption admission once per observed service. The admission verifies the full
// signed runtime archive, so a stack with a dozen services took tens of seconds
// in production and the Companion app timed out. One read must evaluate the
// admission once, however many services the tenant has: the stub admits only
// its first evaluation, so a per-row admission strips adoption from every row
// after the first.
func TestRegistryServicesShareOneAdmissionPerRead(t *testing.T) {
	const services = 12
	store := controlplane.NewMemoryStore()
	ctx, now := t.Context(), time.Now().UTC()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackKitInstanceID: "home-main", Config: map[string]any{"stackkit": "basement-kit"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServerRuntime(ctx, controlplane.ServerRuntime{ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", WorkerID: "agent-1", InventoryRevision: 7, ConnectionState: "connected"}); err != nil {
		t.Fatal(err)
	}
	for i := range services {
		row := controlplane.ServiceRuntime{
			ID: fmt.Sprintf("observed-%02d", i), ServiceKey: fmt.Sprintf("app-%02d", i), ManagementState: "observed",
			TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1",
			Source: "observed", ObservedState: "running", ObservedAt: &now,
			Metadata: map[string]any{"container_id": strings.Repeat(fmt.Sprintf("%x", i), 64)[:64], "inventory_revision": int64(7)},
		}
		if _, err := store.UpsertServiceRuntime(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	var evaluations atomic.Int32
	pin := &agentpb.StackKitReleasePin{Version: "v0.50.9", PlatformOs: "linux", PlatformArch: "amd64", ArchiveSha256: strings.Repeat("a", 64), ReleaseIndexSha256: strings.Repeat("b", 64)}
	projector := &serviceRuntimeHandlers{
		store: store, stacks: store, servers: store, jobs: store, now: func() time.Time { return now },
		applicationAdmission: func() (*agentpb.StackKitReleasePin, error) {
			if evaluations.Add(1) > 1 {
				return nil, errors.New("admission evaluated again within one read")
			}
			return pin, nil
		},
	}
	h := registryRouteHandlers{
		stackStore: store, workerStore: store, registryStore: store, jobStore: store, serverStore: store, serviceStore: store,
		serviceProjector: projector,
	}

	e, w := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/registry/services", "owner-1", "tenant-1", nil)
	if err := h.services(e); err != nil {
		t.Fatal(err)
	}

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data registryPayload `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Services) < services {
		t.Fatalf("registry answered %d of %d services", len(envelope.Data.Services), services)
	}
	for _, service := range envelope.Data.Services {
		if !containsString(service.AllowedActions, serviceActionAdopt) {
			t.Fatalf("service %s lost its adoption action: the read evaluated the admission per row", service.ID)
		}
	}
}
