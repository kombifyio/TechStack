package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/middleware"
)

const runtimeSummaryTestSecret = "runtime-summary-servicecall-test-secret"

func runtimeSummaryCloudToken(t *testing.T, obo *servicecall.OnBehalfOf) string {
	t.Helper()
	token, err := servicecall.IssueToken(
		servicecall.Config{ServiceName: runtimeSummaryCallerCloud, Secret: runtimeSummaryTestSecret, TokenTTL: time.Minute},
		runtimeSummaryService,
		obo,
		"runtime-summary-request",
	)
	if err != nil {
		t.Fatalf("issue service token: %v", err)
	}
	return token
}

func runtimeSummaryTestEvent(target, token string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set(servicecall.HeaderServiceAuth, token)
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

func seedRuntimeSummaryStore(t *testing.T, now time.Time) controlplane.InventoryReadStore {
	t.Helper()
	store := controlplane.NewMemoryStore()
	observedAt := now.Add(-10 * time.Second)
	for _, stack := range []controlplane.CreateStackRequest{
		{ID: "stack-owner", TenantID: "tenant-1", OwnerSubjectID: "auth0|owner", Name: "Owner"},
		{ID: "stack-foreign", TenantID: "tenant-1", OwnerSubjectID: "auth0|other", Name: "Foreign"},
	} {
		if _, err := store.CreateStack(t.Context(), stack); err != nil {
			t.Fatal(err)
		}
	}
	for _, server := range []controlplane.ServerRuntime{
		{ID: "server-owner", TenantID: "tenant-1", StackID: "stack-owner", OwnerSubjectID: "auth0|owner", Name: "Home box", LifecycleState: "active", HealthState: "healthy", LastHeartbeatAt: &observedAt, Metadata: map[string]any{"provider_id": "local", "credential_ref": "never-return"}},
		{ID: "server-foreign", TenantID: "tenant-1", StackID: "stack-foreign", OwnerSubjectID: "auth0|other", Name: "Foreign box", LastHeartbeatAt: &observedAt},
	} {
		if _, err := store.UpsertServerRuntime(t.Context(), server); err != nil {
			t.Fatal(err)
		}
	}
	for _, service := range []controlplane.ServiceRuntime{
		{ID: "service-owner", TenantID: "tenant-1", StackID: "stack-owner", ServerID: "server-owner", ServiceKey: "nextcloud", Name: "Nextcloud", ObservedState: "running", HealthState: "healthy", ObservedAt: &observedAt, Source: "stackkits-inventory", Metadata: map[string]any{"token": "never-return"}},
		{ID: "service-foreign", TenantID: "tenant-1", StackID: "stack-foreign", ServerID: "server-foreign", ServiceKey: "foreign", Name: "Foreign service", ObservedAt: &observedAt},
	} {
		if _, err := store.UpsertServiceRuntime(t.Context(), service); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func TestInternalRuntimeSummaryIsOwnerScopedFromSignedCloudIdentity(t *testing.T) {
	now := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	h := newTestInventoryHandlers(seedRuntimeSummaryStore(t, now), func() time.Time { return now })
	h.serviceAuthSecret = runtimeSummaryTestSecret

	token := runtimeSummaryCloudToken(t, &servicecall.OnBehalfOf{Sub: "auth0|owner", OrgID: "tenant-1"})
	e, rec := runtimeSummaryTestEvent("/api/v1/internal/runtime/summary", token)
	if err := h.httpInternalRuntimeSummary(e); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Fatalf("expected private no-cache, got %q", got)
	}
	body := rec.Body.String()
	for _, forbidden := range []string{"server-foreign", "service-foreign", "never-return", "credential_ref"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, body)
		}
	}
	var summary struct {
		Version string `json:"version"`
		Servers struct {
			Servers []struct {
				ID string `json:"id"`
			}
		} `json:"servers"`
		Services struct{ Services []struct{ ID, Key string } } `json:"services"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if summary.Version != "1" || len(summary.Servers.Servers) != 1 || summary.Servers.Servers[0].ID != "server-owner" {
		t.Fatalf("unexpected servers projection: %s", body)
	}
	if len(summary.Services.Services) != 1 || summary.Services.Services[0].Key != "nextcloud" {
		t.Fatalf("unexpected services projection: %s", body)
	}
}

func TestInternalRuntimeSummaryFailsClosedWithoutCloudIdentity(t *testing.T) {
	now := time.Date(2026, 9, 7, 18, 0, 0, 0, time.UTC)
	h := newTestInventoryHandlers(seedRuntimeSummaryStore(t, now), func() time.Time { return now })

	// No service secret configured: the route is unavailable, not open.
	e, rec := runtimeSummaryTestEvent("/api/v1/internal/runtime/summary", "")
	if err := h.httpInternalRuntimeSummary(e); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without secret, got %d", rec.Code)
	}

	h.serviceAuthSecret = runtimeSummaryTestSecret
	cases := map[string]struct {
		token  string
		target string
		want   int
	}{
		"missing token":        {token: "", target: "/api/v1/internal/runtime/summary", want: http.StatusUnauthorized},
		"no on_behalf_of":      {token: runtimeSummaryCloudToken(t, nil), target: "/api/v1/internal/runtime/summary", want: http.StatusForbidden},
		"missing org":          {token: runtimeSummaryCloudToken(t, &servicecall.OnBehalfOf{Sub: "auth0|owner"}), target: "/api/v1/internal/runtime/summary", want: http.StatusForbidden},
		"scope override query": {token: runtimeSummaryCloudToken(t, &servicecall.OnBehalfOf{Sub: "auth0|owner", OrgID: "tenant-1"}), target: "/api/v1/internal/runtime/summary?tenant_id=tenant-2", want: http.StatusForbidden},
	}
	for name, tc := range cases {
		e, rec := runtimeSummaryTestEvent(tc.target, tc.token)
		if err := h.httpInternalRuntimeSummary(e); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if rec.Code != tc.want {
			t.Fatalf("%s: expected %d, got %d: %s", name, tc.want, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "server-owner") {
			t.Fatalf("%s: denied response must not leak inventory", name)
		}
	}
}

// Regression: missing stored entitlement context previously became a false empty 200.
func TestInternalRuntimeSummaryUsesStoredGrantsWithoutWideningFGA(t *testing.T) {
	now := time.Date(2026, 9, 13, 18, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name             string
		grants, relation bool
		want             int
	}{
		{"authorized owner", true, true, http.StatusOK},
		{"missing grants", false, true, http.StatusForbidden},
		{"revoked FGA relation", true, false, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestInventoryHandlers(seedRuntimeSummaryStore(t, now), func() time.Time { return now })
			h.app.policy = NewInventoryFGAPolicy(&fakeInventoryRelationshipChecker{allowed: tc.relation})
			h.serviceAuthSecret = runtimeSummaryTestSecret
			h.runtimeSummaryContext = func(ctx context.Context, tenant, subject string) (context.Context, error) {
				if tenant != "tenant-1" || subject != "auth0|owner" {
					t.Fatal("OBO scope changed")
				}
				if tc.grants {
					return middleware.WithMembershipEntitlements(ctx, InventoryEntitlementRead), nil
				}
				return ctx, nil
			}
			e, rec := runtimeSummaryTestEvent("/api/v1/internal/runtime/summary", runtimeSummaryCloudToken(t, &servicecall.OnBehalfOf{Sub: "auth0|owner", OrgID: "tenant-1"}))
			if err := h.httpInternalRuntimeSummary(e); err != nil {
				t.Fatal(err)
			}
			if rec.Code != tc.want {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if strings.Contains(body, "server-foreign") || strings.Contains(body, "service-foreign") {
				t.Fatal("foreign inventory leaked")
			}
			if tc.want == http.StatusOK {
				if !strings.Contains(body, "server-owner") || !strings.Contains(body, "service-owner") {
					t.Fatal("owned inventory missing")
				}
			} else if strings.Contains(body, "server-owner") {
				t.Fatal("denial leaked inventory")
			}
		})
	}
}
