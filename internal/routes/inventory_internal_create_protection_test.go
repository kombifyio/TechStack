package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/kombifyio/techstack/internal/providercontrol"
)

func TestCreateProtectionReadUsesAuthorizedAccount(t *testing.T) {
	called := false
	h := inventoryHandlers{
		serviceAuthSecret: runtimeSummaryTestSecret,
		runtimeSummaryContext: func(ctx context.Context, tenant, owner string) (context.Context, string, error) {
			if tenant != "tenant-1" || owner != "auth0|owner" {
				t.Fatal("unsigned principal reached authorization")
			}
			return ctx, tenant, nil
		},
		createProtection: func(_ context.Context, tenant, provider, region string) (providercontrol.CreateProtection, error) {
			called = true
			if tenant != "tenant-1" || provider != "ionos" || region != "de/fra" {
				t.Fatal("projection scope changed")
			}
			return providercontrol.CreateProtection{ProviderID: provider, Region: region, State: "paused", ReasonCode: "provider_create_failure_pause"}, nil
		},
	}
	token := runtimeSummaryCloudToken(t, &servicecall.OnBehalfOf{Sub: "auth0|owner", OrgID: "tenant-1"})
	e, rec := runtimeSummaryTestEvent("/internal/v1/runtime/creation-pauses?provider_id=ionos&region=de%2Ffra", token)
	if err := h.httpInternalCreateProtection(e); err != nil {
		t.Fatal(err)
	}
	var p providercontrol.CreateProtection
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || p.State != "paused" || !called {
		t.Fatalf("projection response: %d %s", rec.Code, rec.Body.String())
	}
	called = false
	e, rec = runtimeSummaryTestEvent("/internal/v1/runtime/creation-pauses?provider_id=ionos&region=de%2Ffra&tenant_id=foreign", token)
	if err := h.httpInternalCreateProtection(e); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusForbidden || called {
		t.Fatalf("scope override reached authority: %d", rec.Code)
	}
}
