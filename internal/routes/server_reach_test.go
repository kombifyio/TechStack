package routes

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/guardreach"
)

type recordingReach struct {
	ServerReachReader
	set []guardreach.PublicReach
}

func (r *recordingReach) ListByTenant(context.Context, string) ([]guardreach.Row, error) {
	return nil, nil
}

func (r *recordingReach) SetPublic(_ context.Context, _, _, _ string, want guardreach.PublicReach) (*guardreach.Row, error) {
	r.set = append(r.set, want)
	return &guardreach.Row{Desired: guardreach.DesiredActive, Public: want}, nil
}

// Publishing a server at a public address is the owner's decision: another
// owner cannot make it, enabling needs the entitlement and a fresh second
// factor and an explicit, valid service and port, and withdrawing is never
// gated.
func TestPublishingAServerNeedsTheOwnerEntitlementStepUpAndAnExplicitPort(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", WorkerID: "worker-1",
		Name: "Node", LifecycleState: "active", ConnectionState: "connected", HealthState: "healthy",
	}); err != nil {
		t.Fatal(err)
	}
	reach := &recordingReach{}
	call := func(owner string, gate posturePolicyGate, body map[string]any) int {
		h := serverRuntimeHandlers{store: store, now: func() time.Time { return now }, policy: NewSelfHostedInventoryPolicy(), reach: reach, postureEntitlements: gate}
		event, recorder := registryRouteStoreTestEvent(http.MethodPut, "/api/v1/servers/server-1/reach", owner, "tenant-1", body)
		event.Request.SetPathValue("serverId", "server-1")
		_ = h.reachSetPublic(event)
		return recorder.Code
	}
	entitled := posturePolicyGate{entitled: true, fresh: true}
	enable := map[string]any{"enabled": true, "service": "grafana", "target_port": 3000}

	if code := call("owner-2", entitled, enable); code != http.StatusNotFound {
		t.Fatalf("another owner got %d", code)
	}
	if code := call("owner-1", posturePolicyGate{entitled: false, fresh: true}, enable); code != http.StatusForbidden {
		t.Fatalf("without the entitlement got %d", code)
	}
	if code := call("owner-1", posturePolicyGate{entitled: true, fresh: false}, enable); code != http.StatusForbidden {
		t.Fatalf("without a fresh second factor got %d", code)
	}
	for name, body := range map[string]map[string]any{
		"no port":         {"enabled": true, "service": "web"},
		"no service":      {"enabled": true, "target_port": 80},
		"another host":    {"enabled": true, "service": "web", "target_port": 80, "target_host": "10.0.0.5"},
		"invalid service": {"enabled": true, "service": "Web Admin!", "target_port": 80},
	} {
		if code := call("owner-1", entitled, body); code != http.StatusBadRequest {
			t.Fatalf("%s got %d", name, code)
		}
	}
	if len(reach.set) != 0 {
		t.Fatalf("a refused request changed the reach: %+v", reach.set)
	}
	if code := call("owner-1", entitled, enable); code != http.StatusOK || len(reach.set) != 1 || reach.set[0].Port != 3000 || !reach.set[0].Enabled {
		t.Fatalf("enable got %d %+v", code, reach.set)
	}
	// Withdrawing needs neither the entitlement nor a second factor.
	if code := call("owner-1", posturePolicyGate{}, map[string]any{"enabled": false}); code != http.StatusOK || len(reach.set) != 2 || reach.set[1].Enabled {
		t.Fatalf("disable got %d %+v", code, reach.set)
	}
}
