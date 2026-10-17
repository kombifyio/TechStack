package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"

	"github.com/kombifyio/techstack/internal/household"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
)

const householdTestSecret = "household-servicecall-test-secret"

type countingIdentity struct {
	calls int
	apps  household.AppGrant
}

func (c *countingIdentity) Invite(_ context.Context, _ household.Target, _, _, _ string, apps household.AppGrant) (household.Invitation, error) {
	c.calls++
	c.apps = apps
	return household.Invitation{}, nil
}

func (c *countingIdentity) UpdateApps(_ context.Context, _ household.Target, _ string, apps household.AppGrant) error {
	c.calls++
	c.apps = apps
	return nil
}

func (c *countingIdentity) Remove(context.Context, household.Target, string) error {
	c.calls++
	return nil
}

type fixedTenant struct{}

func (fixedTenant) ResolveOwnerTenant(context.Context, string, string) (string, error) {
	return "tenant-1", nil
}

// Sensitive boundary: the projection creates a node user on the owner's
// homelab, so only kombify Cloud, with the household projection scope and on
// behalf of the owner, may reach it. Every other caller is refused before the
// node is touched.
func TestHouseholdProjectionRefusesCallersWithoutTheCloudCredential(t *testing.T) {
	store := controlplane.NewMemoryStore()
	identity := &countingIdentity{}
	router := httpx.NewRouter()
	RegisterHouseholdInternalRoutes(router, HouseholdRouteConfig{
		ServiceAuthSecret: householdTestSecret,
		Service: &household.Service{
			Tenants: fixedTenant{}, Homelabs: store, Stacks: store,
			Links: controlplane.NewMemoryHouseholdLinkStore(), Identity: identity,
		},
	})
	token := func(caller, scope string, obo *servicecall.OnBehalfOf) string {
		issued, err := servicecall.IssueToken(
			servicecall.Config{ServiceName: caller, Secret: householdTestSecret, TokenTTL: time.Minute, Scope: scope},
			runtimeSummaryService, obo, "household-test")
		if err != nil {
			t.Fatal(err)
		}
		return issued
	}
	body := `{"owner_subject":"auth0|owner","member":{"subject":"auth0|lena","email":"lena@example.com","display_name":"Lena"},"action":"add"}`
	post := func(header string) int {
		req := httptest.NewRequest(http.MethodPost, householdProjectionPath, strings.NewReader(body))
		if header != "" {
			req.Header.Set(servicecall.HeaderServiceAuth, header)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}
	owner := &servicecall.OnBehalfOf{Sub: "auth0|owner"}
	for name, header := range map[string]string{
		"no credential":          "",
		"garbage credential":     "not-a-token",
		"another service":        token("gateway", HouseholdProjectionScope, owner),
		"cloud without scope":    token(runtimeSummaryCallerCloud, "", owner),
		"cloud with other scope": token(runtimeSummaryCallerCloud, HouseholdLinksReadScope, owner),
		"cloud for another user": token(runtimeSummaryCallerCloud, HouseholdProjectionScope, &servicecall.OnBehalfOf{Sub: "auth0|stranger"}),
		"cloud without a user":   token(runtimeSummaryCallerCloud, HouseholdProjectionScope, nil),
	} {
		if code := post(header); code < 400 || code >= 500 {
			t.Errorf("%s: want a 4xx refusal, got %d", name, code)
		}
	}
	if identity.calls != 0 {
		t.Fatalf("a refused caller reached the node %d times", identity.calls)
	}
	// The owner's own Cloud call passes the credential check (no homelab here,
	// so it ends as "none", not as a refusal).
	if code := post(token(runtimeSummaryCallerCloud, HouseholdProjectionScope, owner)); code != http.StatusAccepted {
		t.Fatalf("the Cloud call on behalf of the owner must be accepted, got %d", code)
	}
}

// Sensitive boundary: the owner's per-member app grants decide which node apps
// a member signs into. The projection hands exactly the granted, de-duplicated
// node app ids to the node and refuses an id outside the grantable catalog
// before the node is touched.
func TestHouseholdProjectionPassesExactlyTheGrantedApps(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	homelab, err := store.CreateHomelab(ctx, controlplane.CreateHomelabRequest{ID: "hl-1", TenantID: "tenant-1", OwnerSubjectID: "auth0|owner", Name: "Home"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "auth0|owner", HomelabID: homelab.ID, Name: "Home", Mode: "standard",
	}); err != nil {
		t.Fatal(err)
	}
	identity := &countingIdentity{}
	router := httpx.NewRouter()
	RegisterHouseholdInternalRoutes(router, HouseholdRouteConfig{
		ServiceAuthSecret: householdTestSecret,
		Service: &household.Service{
			Tenants: fixedTenant{}, Homelabs: store, Stacks: store,
			Links: controlplane.NewMemoryHouseholdLinkStore(), Identity: identity,
		},
	})
	issued, err := servicecall.IssueToken(
		servicecall.Config{ServiceName: runtimeSummaryCallerCloud, Secret: householdTestSecret, TokenTTL: time.Minute, Scope: HouseholdProjectionScope},
		runtimeSummaryService, &servicecall.OnBehalfOf{Sub: "auth0|owner"}, "household-test")
	if err != nil {
		t.Fatal(err)
	}
	post := func(apps string) int {
		body := `{"owner_subject":"auth0|owner","member":{"subject":"auth0|lena","email":"lena@example.com","display_name":"Lena","node_apps":` + apps + `},"action":"add"}`
		req := httptest.NewRequest(http.MethodPost, householdProjectionPath, strings.NewReader(body))
		req.Header.Set(servicecall.HeaderServiceAuth, issued)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post(`["homelab.app.photos","homelab.app.smart-home","homelab.app.photos"]`); code != http.StatusAccepted {
		t.Fatalf("granted apps must be accepted, got %d", code)
	}
	if !identity.apps.Set || strings.Join(identity.apps.Apps, ",") != "photos,smart-home" {
		t.Fatalf("node received %+v, want exactly photos and smart-home", identity.apps)
	}
	identity.calls = 0
	if code := post(`["homelab.app.photos","homelab.app.vault"]`); code != http.StatusBadRequest || identity.calls != 0 {
		t.Fatalf("an ungrantable app must be refused before the node, got %d with %d node calls", code, identity.calls)
	}
}
