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

type countingIdentity struct{ calls int }

func (c *countingIdentity) Invite(context.Context, household.Target, string, string, string) (household.Invitation, error) {
	c.calls++
	return household.Invitation{}, nil
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
