package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/guardposture"
)

type updatesTestPosture struct {
	ServerPosture
	views map[string]guardposture.View
}

func (p updatesTestPosture) Views(context.Context, string) (map[string]guardposture.View, error) {
	return p.views, nil
}

func (p updatesTestPosture) View(*guardposture.Posture) guardposture.View { return guardposture.View{} }

// The updates read carries only the signed owner's servers, projects exactly
// the OS update facts the node's Guard reported, and never turns a missing
// fact or an uncompared service into "up to date".
func TestInternalRuntimeUpdatesProjectsOnlyReportedFactsOfTheOwnersServers(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	store := seedRuntimeSummaryStore(t, now).(*controlplane.MemoryStore)
	observedAt := now.Add(-10 * time.Second)
	for _, server := range []controlplane.ServerRuntime{
		{ID: "server-owner", TenantID: "tenant-1", StackID: "stack-owner", OwnerSubjectID: "auth0|owner", WorkerID: "worker-owner", Name: "Home box", LastHeartbeatAt: &observedAt},
		{ID: "server-silent", TenantID: "tenant-1", StackID: "stack-owner", OwnerSubjectID: "auth0|owner", WorkerID: "worker-silent", Name: "Quiet box", LastHeartbeatAt: &observedAt},
		{ID: "server-unguarded", TenantID: "tenant-1", StackID: "stack-owner", OwnerSubjectID: "auth0|owner", Name: "Bare box", LastHeartbeatAt: &observedAt},
	} {
		if _, err := store.UpsertServerRuntime(t.Context(), server); err != nil {
			t.Fatal(err)
		}
	}
	pending, reboot := 3, true
	h := newTestInventoryHandlers(store, func() time.Time { return now })
	h.serviceAuthSecret = runtimeSummaryTestSecret
	h.posture = updatesTestPosture{views: map[string]guardposture.View{
		"worker-owner":  {Updates: guardposture.Updates{OS: guardposture.OSUpdates{PendingSecurityUpdates: &pending, RebootRequired: &reboot}}},
		"worker-silent": {},
	}}

	token := runtimeSummaryCloudToken(t, &servicecall.OnBehalfOf{Sub: "auth0|owner", OrgID: "tenant-1"})
	e, rec := runtimeSummaryTestEvent("/api/v1/internal/runtime/updates", token)
	if err := h.httpInternalRuntimeUpdates(e); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "server-foreign") {
		t.Fatalf("updates leaked another owner's server: %s", rec.Body.String())
	}
	var body runtimeUpdates
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	byID := map[string]runtimeUpdatesServer{}
	for _, server := range body.Servers {
		byID[server.ServerID] = server
	}
	owner := byID["server-owner"]
	if owner.OS.State != runtimeUpdatesOSReported || owner.OS.PendingSecurityUpdates == nil || *owner.OS.PendingSecurityUpdates != 3 ||
		owner.OS.RebootRequired == nil || !*owner.OS.RebootRequired {
		t.Fatalf("owner os = %+v, want 3 pending security updates and a required reboot", owner.OS)
	}
	// The seeded service has no measured image version: not compared, not current.
	if owner.Services.NotCompared != 1 || owner.Services.Compared != 0 || len(owner.Services.Available) != 0 {
		t.Fatalf("owner services = %+v, want one service not compared", owner.Services)
	}
	if silent := byID["server-silent"].OS; silent.State != runtimeUpdatesOSUnknown || silent.ReasonCode != "update_evidence_unavailable" || silent.PendingSecurityUpdates != nil || silent.RebootRequired != nil {
		t.Fatalf("silent os = %+v, want unknown without facts", silent)
	}
	if bare := byID["server-unguarded"].OS; bare.State != runtimeUpdatesOSUnknown || bare.ReasonCode != "no_guard" {
		t.Fatalf("unguarded os = %+v, want unknown/no_guard", bare)
	}
}
