package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	authsession "github.com/kombifyio/go-common/authsession"
	"github.com/kombifyio/go-common/oidcclient"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

type fakeOrgLister struct {
	orgs  []string
	err   error
	calls int
}

func (f *fakeOrgLister) UserOrganizations(_ context.Context, _ string) ([]string, error) {
	f.calls++
	return f.orgs, f.err
}

func TestV2CloudTenantResolverPrefersDemoTenant(t *testing.T) {
	t.Setenv("TECHSTACK_DEMO_USER_IDS", "auth0|demo-user")
	t.Setenv("TECHSTACK_DEMO_TENANT_ID", "tenant-demo")

	resolver := v2CloudTenantResolver(&stubAuthStore{}, nil, "default")
	tenantID, err := resolver(context.Background(), &oidcclient.Claims{Subject: "auth0|demo-user"}, "primary", "")
	if err != nil || tenantID != "tenant-demo" {
		t.Fatalf("resolver = %q, %v; want tenant-demo", tenantID, err)
	}
}

func TestV2CloudTenantResolverUsesIDTokenOrgClaim(t *testing.T) {
	resolver := v2CloudTenantResolver(&stubAuthStore{}, nil, "default")
	claims := &oidcclient.Claims{
		Subject: "auth0|user-1",
		Raw:     map[string]any{"org_id": "org_claimed"},
	}
	tenantID, err := resolver(context.Background(), claims, "primary", "")
	if err != nil || tenantID != "org_claimed" {
		t.Fatalf("resolver = %q, %v; want org_claimed", tenantID, err)
	}
}

func TestV2CloudTenantResolverPrefersNamespacedKombifyClaim(t *testing.T) {
	// The Auth0 post-login action mints https://kombify.io/org_id from
	// app_metadata; the Cloudflare edge derives x-org-id from the same claim,
	// so the session must resolve the identical tenant (one-truth rule).
	resolver := v2CloudTenantResolver(&stubAuthStore{}, nil, "default")
	claims := &oidcclient.Claims{
		Subject: "auth0|user-1",
		Raw: map[string]any{
			"https://kombify.io/org_id": "org_portal",
			"org_id":                    "org_plain_should_lose",
		},
	}
	tenantID, err := resolver(context.Background(), claims, "primary", "")
	if err != nil || tenantID != "org_portal" {
		t.Fatalf("resolver = %q, %v; want org_portal from the namespaced claim", tenantID, err)
	}
}

func TestV2CloudTenantResolverPrefersOrgMembershipOverDefault(t *testing.T) {
	store := &stubAuthStore{memberships: []controlplane.Membership{
		{TenantID: "default", UserID: "auth0|user-1", RoleKey: "member", Status: "active"},
		{TenantID: "org_acme", UserID: "auth0|user-1", RoleKey: "member", Status: "active"},
	}}
	lister := &fakeOrgLister{orgs: []string{"org_should_not_be_used"}}
	resolver := v2CloudTenantResolver(store, lister, "default")
	tenantID, err := resolver(context.Background(), &oidcclient.Claims{Subject: "auth0|user-1"}, "primary", "")
	if err != nil || tenantID != "org_acme" {
		t.Fatalf("resolver = %q, %v; want org_acme from memberships", tenantID, err)
	}
	if lister.calls != 0 {
		t.Fatalf("management API consulted %d times despite membership hit", lister.calls)
	}
}

func TestV2CloudTenantResolverFallsBackToManagementAPI(t *testing.T) {
	lister := &fakeOrgLister{orgs: []string{"org_bravo", "org_alpha"}}
	resolver := v2CloudTenantResolver(&stubAuthStore{}, lister, "default")
	tenantID, err := resolver(context.Background(), &oidcclient.Claims{Subject: "auth0|user-1"}, "primary", "")
	if err != nil || tenantID != "org_alpha" {
		t.Fatalf("resolver = %q, %v; want deterministic org_alpha", tenantID, err)
	}
}

func TestV2CloudTenantResolverKeepsExistingMembershipTenant(t *testing.T) {
	// Provisioned users without an org (e2e/test accounts on the shared
	// default tenant) keep their tenant; only membership-less users fall
	// through to the owner tenant.
	store := &stubAuthStore{memberships: []controlplane.Membership{
		{TenantID: "default", UserID: "auth0|user-1", RoleKey: "member", Status: "active"},
	}}
	resolver := v2CloudTenantResolver(store, &fakeOrgLister{orgs: []string{"org_unused"}}, "default")
	tenantID, err := resolver(context.Background(), &oidcclient.Claims{Subject: "auth0|user-1"}, "primary", "")
	if err != nil || tenantID != "default" {
		t.Fatalf("resolver = %q, %v; want existing default membership kept", tenantID, err)
	}
}

func TestV2CloudTenantResolverFallsBackToOwnerTenant(t *testing.T) {
	// No org claim, no membership, Management API down: the login still
	// succeeds and resolves the deterministic owner tenant — the same tenant
	// the data-plane fallback yields, so both surfaces agree.
	lister := &fakeOrgLister{err: errors.New("mgmt down")}
	resolver := v2CloudTenantResolver(&stubAuthStore{}, lister, "tenant-1")
	tenantID, err := resolver(context.Background(), &oidcclient.Claims{Subject: "auth0|user-1"}, "primary", "ignored-request-tenant")
	if err != nil || tenantID != "usr:auth0|user-1" {
		t.Fatalf("resolver = %q, %v; want owner tenant usr:auth0|user-1", tenantID, err)
	}
}

func TestOwnerTenantIDUsesGatewayNamespaceAndKeepsLegacyReadCompatibility(t *testing.T) {
	const subject = "auth0|user-1"
	if got := canonicalOwnerTenantID(subject); got != "usr:auth0|user-1" {
		t.Fatalf("canonicalOwnerTenantID() = %q", got)
	}
	if !isOwnerTenantID("usr:auth0|user-1", subject) {
		t.Fatal("Gateway owner tenant must be accepted")
	}
	if !isOwnerTenantID(subject, subject) {
		t.Fatal("legacy naked owner tenant must remain readable")
	}
	if isOwnerTenantID("usr:auth0|other-user", subject) {
		t.Fatal("foreign personal tenant must be rejected")
	}
}

func TestV2CloudUserUpsertPersistsResolvedOrgTenant(t *testing.T) {
	store := &stubAuthStore{}
	claims := &oidcclient.Claims{
		Subject: "auth0|user-1",
		Email:   "user-1@kombify.io",
	}
	if err := v2CloudUserUpsert(store, "default")(context.Background(), claims, "org_acme", "primary"); err != nil {
		t.Fatalf("v2CloudUserUpsert() error = %v", err)
	}
	if len(store.tenants) != 2 || store.tenants[0].ID != "org_acme" || store.tenants[1].ID != "default" {
		t.Fatalf("UpsertTenant calls = %#v, want org_acme then default", store.tenants)
	}
	if store.tenants[0].Kind != "saas" || store.tenants[0].ExternalOrgID != "org_acme" {
		t.Fatalf("org tenant record = %#v, want kind=saas external_org_id=org_acme", store.tenants[0])
	}
	if len(store.memberships) != 2 ||
		store.memberships[0].TenantID != "org_acme" ||
		store.memberships[1].TenantID != "default" {
		t.Fatalf("memberships = %#v, want org_acme membership plus default rollover membership", store.memberships)
	}
}

func TestEnrichV2SessionIdentityPrefersOrgMembershipInSaaS(t *testing.T) {
	now := time.Now().UTC()
	store := &stubAuthStore{memberships: []controlplane.Membership{
		{TenantID: "default", UserID: "auth0|user-1", RoleKey: "member", Status: "active", UpdatedAt: now},
		{TenantID: "org_acme", UserID: "auth0|user-1", RoleKey: "admin", Status: "active", UpdatedAt: now},
	}}
	claims := &authsession.Claims{Subject: "auth0|user-1", Email: "user-1@kombify.io"}
	id := identityFromV2SessionClaims(claims)

	membership := enrichV2SessionIdentityFromMembership(context.Background(), store, "default", true, claims, id)
	if membership == nil || membership.TenantID != "org_acme" {
		t.Fatalf("membership = %#v, want org_acme preferred", membership)
	}
	hydrateIdentityTenantFromMembership(id, membership)
	if id.OrgID != "org_acme" {
		t.Fatalf("identity.OrgID = %q, want org_acme", id.OrgID)
	}
}

func TestEnrichV2SessionIdentityKeepsDefaultChainInSelfHosted(t *testing.T) {
	store := &stubAuthStore{membership: &controlplane.Membership{
		TenantID: "default", UserID: "auth0|user-1", RoleKey: "member", Status: "active",
	}}
	claims := &authsession.Claims{Subject: "auth0|user-1"}
	id := identityFromV2SessionClaims(claims)

	membership := enrichV2SessionIdentityFromMembership(context.Background(), store, "default", false, claims, id)
	if membership == nil || membership.TenantID != "default" {
		t.Fatalf("membership = %#v, want default chain untouched in self-hosted mode", membership)
	}
}

func TestV2SessionIdentityMiddlewareResolvesOwnerTenantForOrglessSaaSEdgeIdentity(t *testing.T) {
	// An org-less gateway identity without any control-plane membership must
	// resolve its own subject as tenant — the same tenant on every surface —
	// and get its personal tenant materialized (one-truth rule).
	store := &stubAuthStore{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stacks", nil)
	edgeIdentity := &identity.Identity{UserID: "auth0|user-1", Email: "user-1@kombify.io"}
	req = req.WithContext(identity.NewContext(req.Context(), edgeIdentity))
	event := &httpx.Event{Request: req, Response: httptest.NewRecorder()}

	boot := &v2Boot{session: mustSessionManager(t), authStore: store, defaultTenant: "default", cookieName: "techstack_session", saasMode: true}
	if err := v2SessionIdentityMiddleware(boot)(event); err != nil {
		t.Fatalf("v2SessionIdentityMiddleware() error = %v", err)
	}
	id := identity.FromContext(event.Request.Context())
	if id == nil || id.OrgID != "usr:auth0|user-1" {
		t.Fatalf("identity = %+v, want owner tenant usr:auth0|user-1", id)
	}
	if len(store.memberships) != 1 || store.memberships[0].TenantID != "usr:auth0|user-1" {
		t.Fatalf("memberships = %#v, want bootstrapped owner-tenant membership", store.memberships)
	}
}

func TestV2SessionIdentityMiddlewareKeepsMembershipTenantForOrglessEdgeIdentity(t *testing.T) {
	// A provisioned user (existing default membership) keeps that tenant;
	// the owner fallback only applies when no membership resolves.
	store := &stubAuthStore{membership: &controlplane.Membership{
		TenantID: "default", UserID: "auth0|user-1", RoleKey: "member", Status: "active",
	}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stacks", nil)
	edgeIdentity := &identity.Identity{UserID: "auth0|user-1", Email: "user-1@kombify.io"}
	req = req.WithContext(identity.NewContext(req.Context(), edgeIdentity))
	event := &httpx.Event{Request: req, Response: httptest.NewRecorder()}

	boot := &v2Boot{session: mustSessionManager(t), authStore: store, defaultTenant: "default", cookieName: "techstack_session", saasMode: true}
	if err := v2SessionIdentityMiddleware(boot)(event); err != nil {
		t.Fatalf("v2SessionIdentityMiddleware() error = %v", err)
	}
	id := identity.FromContext(event.Request.Context())
	if id == nil || id.OrgID != "default" {
		t.Fatalf("identity = %+v, want membership tenant default", id)
	}
}

func mustSessionManager(t *testing.T) *authsession.Manager {
	t.Helper()
	mgr, err := authsession.NewManager(authsession.Config{
		Issuer:   "techstack",
		Audience: "techstack-runtime-e2e",
		Secret:   []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return mgr
}
