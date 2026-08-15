package tenant

import (
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
)

func TestEnforcer_IsActive(t *testing.T) {
	tests := []struct {
		mode   config.DeploymentMode
		active bool
	}{
		{config.ModeSelfHosted, false},
		{config.ModeSaaS, true},
	}
	for _, tt := range tests {
		e := NewEnforcer(tt.mode)
		if e.IsActive() != tt.active {
			t.Errorf("mode=%q: IsActive()=%v, want %v", tt.mode, e.IsActive(), tt.active)
		}
	}
}

func TestEnforcer_ScopeFilterParams_SelfHosted(t *testing.T) {
	e := NewEnforcer(config.ModeSelfHosted)
	filter, params := e.ScopeFilterParams(nil)
	if filter != "1 = 1" {
		t.Errorf("filter = %q, want %q", filter, "1 = 1")
	}
	if params != nil {
		t.Errorf("params = %v, want nil", params)
	}
}

// TestEnforcer_ScopeFilterParams_SaaSMissingIdentity locks in the
// fail-closed contract: in SaaS mode without an identity, the filter
// must deny all rows. Any other behavior (empty, "1 = 1") is an IDOR.
func TestEnforcer_ScopeFilterParams_SaaSMissingIdentity(t *testing.T) {
	e := NewEnforcer(config.ModeSaaS)
	// Pass nil RequestEvent: TenantID() will bail out before dereffing,
	// simulating the "SaaS mode but identity middleware didn't run" case.
	filter, params := e.ScopeFilterParams(nil)
	if filter != "0 = 1" {
		t.Errorf("saas without identity must fail closed: filter = %q, want %q", filter, "0 = 1")
	}
	if params != nil {
		t.Errorf("params = %v, want nil", params)
	}
}

// TestEnforcer_ScopeFilter_SaaSMissingIdentity covers the non-params
// variant with the same contract. Self-hosted still returns "" (caller
// skips filter); SaaS without identity must not.
func TestEnforcer_ScopeFilter_SaaSMissingIdentity(t *testing.T) {
	e := NewEnforcer(config.ModeSaaS)
	filter := e.ScopeFilter(nil)
	if filter != "0 = 1" {
		t.Errorf("saas without identity must fail closed: filter = %q, want %q", filter, "0 = 1")
	}
}

func TestEnforcer_ScopeFilter_SelfHosted(t *testing.T) {
	e := NewEnforcer(config.ModeSelfHosted)
	if got := e.ScopeFilter(nil); got != "" {
		t.Errorf("self-hosted ScopeFilter = %q, want empty", got)
	}
}

func TestEnforcer_ScopeFilterParams_SaaS(t *testing.T) {
	_ = NewEnforcer(config.ModeSaaS)

	// Create a fake request with identity in context
	req := httptest.NewRequest("GET", "/test", nil)
	id := &identity.Identity{
		UserID: "user-1",
		OrgID:  "org-42",
		Email:  "user@example.com",
	}
	ctx := identity.NewContext(req.Context(), id)
	req = req.WithContext(ctx)

	// We can't test with core.RequestEvent directly (needs PocketBase),
	// but we can test the identity extraction
	extractedID := identity.FromContext(ctx)
	if extractedID == nil {
		t.Fatal("expected identity in context")
	}
	if extractedID.OrgID != "org-42" {
		t.Errorf("OrgID = %q, want %q", extractedID.OrgID, "org-42")
	}
}

func TestEnforcer_VerifyAccess_SelfHosted(t *testing.T) {
	e := NewEnforcer(config.ModeSelfHosted)
	// Self-hosted always allows
	if !e.VerifyAccess(nil, nil) {
		t.Error("self-hosted mode should always allow access")
	}
}

func TestEnforcer_VerifyAccess_SaaS(t *testing.T) {
	e := NewEnforcer(config.ModeSaaS)
	event := tenantRequestEvent("org-42")

	matching := tenantRecord("org-42")
	if !e.VerifyAccess(event, matching) {
		t.Fatal("matching tenant should be allowed")
	}

	wrongTenant := tenantRecord("org-99")
	if e.VerifyAccess(event, wrongTenant) {
		t.Fatal("wrong tenant should be denied")
	}

	tenantless := tenantRecord("")
	if e.VerifyAccess(event, tenantless) {
		t.Fatal("tenantless SaaS record should fail closed")
	}
	if !e.VerifyLegacyTenantlessAccess(event, tenantless) {
		t.Fatal("explicit legacy tenantless compatibility helper should allow tenantless record")
	}
}

func tenantRequestEvent(orgID string) *core.RequestEvent {
	req := httptest.NewRequest("GET", "/test", nil)
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{
		UserID: "user-1",
		OrgID:  orgID,
	}))
	return &core.RequestEvent{
		Event: router.Event{
			Request: req,
		},
	}
}

func tenantRecord(tenantID string) *core.Record {
	record := core.NewRecord(core.NewBaseCollection("stacks"))
	record.Set("tenant_id", tenantID)
	return record
}

func TestEscapePBFilter(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"it's", "it''s"},
		{"a''b", "a''''b"},
		{"", ""},
	}
	for _, tt := range tests {
		got := escapePBFilter(tt.input)
		if got != tt.want {
			t.Errorf("escapePBFilter(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
