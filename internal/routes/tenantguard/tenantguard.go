// Package tenantguard enforces the SaaS one-truth rule for tenant-scoped
// data-plane handlers: a SaaS request without an explicit organization scope
// must fail closed with an actionable denial instead of silently serving the
// legacy path or the shared default tenant (which renders as an empty
// dashboard that contradicts the embedded portal view).
package tenantguard

import (
	"strings"
	"sync"

	"github.com/kombifyio/techstack/pkg/httpx"
)

var state struct {
	mu   sync.RWMutex
	saas bool
}

// Configure sets the deployment posture once at route wiring. Self-hosted
// (the default) keeps every legacy fallback; SaaS activates the fail-closed
// denial for tenant-less requests.
func Configure(saasMode bool) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.saas = saasMode
}

// Active reports whether the SaaS fail-closed posture is enabled.
func Active() bool {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.saas
}

// RequireTenant returns the fail-closed denial when SaaS mode is active and
// the request carries no tenant scope; nil otherwise.
func RequireTenant(tenantID, capability string) error {
	if !Active() || strings.TrimSpace(tenantID) != "" {
		return nil
	}
	return Denial(capability)
}

// TenantScope resolves the effective tenant for a tenant-scoped handler. In
// SaaS mode only the explicit request tenant counts (fail-closed when
// absent); self-hosted keeps the explicit-then-fallback behavior.
func TenantScope(explicitTenantID, fallback, capability string) (string, error) {
	explicit := strings.TrimSpace(explicitTenantID)
	if Active() {
		if explicit == "" {
			return "", Denial(capability)
		}
		return explicit, nil
	}
	if explicit != "" {
		return explicit, nil
	}
	return strings.TrimSpace(fallback), nil
}

// Denial is the structured tenant_context_required error
// (FEATURE-ENTITLEMENT-UX-STANDARD envelope). The frontend localizes by
// error_code; the embedded strings are the technical fallback.
func Denial(capability string) *httpx.APIError {
	return httpx.NewForbiddenError("Organization context required", map[string]any{
		"error_code":       "tenant_context_required",
		"reason_code":      "tenant_context_missing",
		"capability":       capability,
		"required_context": []string{"org_id"},
		"retryable":        false,
		"user_guidance": map[string]any{
			"title": "Organization context required",
			"body":  "Your session carries no kombify organization, so tenant data cannot be shown. This protects you from seeing different data here than in kombify Cloud.",
			"next_steps": []string{
				"Sign in again at techstack.kombify.io",
				"Or open TechStack from your kombify Cloud portal",
			},
		},
		"remediation":     "Verify the SPA requests an organization-scoped gateway token and the user has an organization membership; check the login tenant-resolver logs.",
		"support_context": map[string]any{"feature_source": "auth0-org/edge-identity", "cost_bearing": false},
	})
}
