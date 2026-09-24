package routes

import (
	"context"
	"net/http"
	"testing"

	"github.com/kombifyio/techstack/internal/substrateprovision"
)

type applianceProvisionerFunc func(context.Context, substrateprovision.Request) (substrateprovision.Result, error)

func (f applianceProvisionerFunc) Prepare(ctx context.Context, r substrateprovision.Request) (substrateprovision.Result, error) {
	return f(ctx, r)
}

// Provider-control boundary: caller input never supplies tenancy, owner or an
// Ubuntu profile to the appliance path, and a valid request uses native admission.
func TestApplianceCreationUsesAuthenticatedNativeAdmission(t *testing.T) {
	calls := 0
	h := substrateApplianceRoutes{provisioner: applianceProvisionerFunc(func(_ context.Context, r substrateprovision.Request) (substrateprovision.Result, error) {
		calls++
		if r.TenantID != "tenant-1" || r.OwnerID != "owner-1" || r.Placement.ProfileID != "haos" || r.IdempotencyKey != "haos-1" {
			t.Fatalf("wrong admission authority: %+v", r)
		}
		return substrateprovision.Result{LeaseID: "lease", OperationID: "operation", ProfileID: "haos"}, nil
	})}
	for _, test := range []struct {
		profile string
		forge   bool
		want    int
	}{{"ubuntu-24.04", false, 400}, {"haos", true, 400}, {"haos", false, 202}} {
		body := map[string]any{"stack_id": "stack", "node_id": "haos", "placement": map[string]any{"server_id": "substrate", "profile_id": test.profile, "storage": "local", "bridge": "vmbr0", "cpu": 2, "memory_mib": 4096, "disk_gib": 32}}
		if test.forge {
			body["owner_id"] = "different-owner"
		}
		e, rr := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/substrates/substrate/appliances", "owner-1", "tenant-1", body)
		e.Request.SetPathValue("id", "substrate")
		e.Request.Header.Set("Idempotency-Key", "haos-1")
		if err := h.create(e); err != nil {
			t.Fatal(err)
		}
		if rr.Code != test.want {
			t.Fatalf("HTTP %d: %s", rr.Code, rr.Body.String())
		}
	}
	if calls != 1 {
		t.Fatal("untrusted placement reached native admission")
	}
}
