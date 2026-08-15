package tenantguard

import (
	"net/http"
	"testing"
)

func withSaaS(t *testing.T, saas bool) {
	t.Helper()
	Configure(saas)
	t.Cleanup(func() { Configure(false) })
}

func TestRequireTenantSelfHostedAllowsEmptyTenant(t *testing.T) {
	withSaaS(t, false)
	if err := RequireTenant("", "techstack.stacks.list"); err != nil {
		t.Fatalf("RequireTenant() = %v, want nil in self-hosted mode", err)
	}
}

func TestRequireTenantSaaSDeniesEmptyTenant(t *testing.T) {
	withSaaS(t, true)
	err := RequireTenant("  ", "techstack.stacks.list")
	if err == nil {
		t.Fatal("RequireTenant() = nil, want tenant_context_required denial")
	}
	assertDenialEnvelope(t, err, "techstack.stacks.list")
}

func TestRequireTenantSaaSAllowsExplicitTenant(t *testing.T) {
	withSaaS(t, true)
	if err := RequireTenant("org_acme", "techstack.stacks.list"); err != nil {
		t.Fatalf("RequireTenant() = %v, want nil for explicit tenant", err)
	}
}

func TestTenantScopeSelfHostedKeepsFallback(t *testing.T) {
	withSaaS(t, false)
	tenantID, err := TenantScope("", "owner-1", "techstack.workers.list")
	if err != nil || tenantID != "owner-1" {
		t.Fatalf("TenantScope() = %q, %v; want owner-1 fallback", tenantID, err)
	}
	tenantID, err = TenantScope("org_acme", "owner-1", "techstack.workers.list")
	if err != nil || tenantID != "org_acme" {
		t.Fatalf("TenantScope() = %q, %v; want explicit org_acme", tenantID, err)
	}
}

func TestTenantScopeSaaSFailsClosedWithoutExplicitTenant(t *testing.T) {
	withSaaS(t, true)
	if _, err := TenantScope("", "owner-1", "techstack.workers.list"); err == nil {
		t.Fatal("TenantScope() accepted the owner fallback in SaaS mode")
	} else {
		assertDenialEnvelope(t, err, "techstack.workers.list")
	}
	tenantID, err := TenantScope("org_acme", "owner-1", "techstack.workers.list")
	if err != nil || tenantID != "org_acme" {
		t.Fatalf("TenantScope() = %q, %v; want explicit org_acme", tenantID, err)
	}
}

func assertDenialEnvelope(t *testing.T, err error, capability string) {
	t.Helper()
	denial, ok := err.(interface {
		Error() string
	})
	if !ok || denial == nil {
		t.Fatalf("denial is not an error: %#v", err)
	}
	apiErr := Denial(capability)
	if apiErr.Status != http.StatusForbidden {
		t.Fatalf("Denial status = %d, want 403", apiErr.Status)
	}
	details, ok := apiErr.Details.(map[string]any)
	if !ok {
		t.Fatalf("Denial details = %#v, want map", apiErr.Details)
	}
	if details["error_code"] != "tenant_context_required" {
		t.Fatalf("error_code = %v, want tenant_context_required", details["error_code"])
	}
	if details["reason_code"] != "tenant_context_missing" {
		t.Fatalf("reason_code = %v, want tenant_context_missing", details["reason_code"])
	}
	if details["capability"] != capability {
		t.Fatalf("capability = %v, want %s", details["capability"], capability)
	}
	if details["retryable"] != false {
		t.Fatalf("retryable = %v, want false", details["retryable"])
	}
	guidance, ok := details["user_guidance"].(map[string]any)
	if !ok || guidance["title"] == "" || guidance["body"] == "" {
		t.Fatalf("user_guidance = %#v, want title and body", details["user_guidance"])
	}
	steps, ok := guidance["next_steps"].([]string)
	if !ok || len(steps) == 0 {
		t.Fatalf("next_steps = %#v, want non-empty", guidance["next_steps"])
	}
}
