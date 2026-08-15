package stacks

import (
	"strings"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/demoguard"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/pocketbase/pocketbase/core"
)

// demoRestrictedStackRequest reports whether the request principal belongs to
// the shared public demo account. Inert without demo env configuration.
func demoRestrictedStackRequest(e *httpx.Event) bool {
	tenantID := tenantIDFromRequest(e)
	userID := ""
	if e != nil && e.Request != nil {
		if id := identity.FromContext(e.Request.Context()); id != nil {
			userID = id.UserID
		}
	}
	return demoguard.IsDemoSubject(tenantID, userID)
}

// demoProtectedStoreStackRequest narrows the shared-account restriction to the
// one explicitly marked anchor. Visitors must still be able to clean up their
// own failed or legacy attempts in the demo tenant.
func demoProtectedStoreStackRequest(e *httpx.Event, stack *controlplane.Stack) bool {
	return stack != nil &&
		demoRestrictedStackRequest(e) &&
		boolFromMaps("demo_anchor", stack.RuntimeSummary, stack.Config)
}

// demoProtectedLegacyStackRequest preserves the reserved pre-migration anchor
// name without turning every old row owned by a demo visitor into an undeletable
// record.
func demoProtectedLegacyStackRequest(e *httpx.Event, stack *core.Record) bool {
	return stack != nil &&
		demoRestrictedStackRequest(e) &&
		strings.EqualFold(strings.TrimSpace(stack.GetString("name")), "kombify-demo")
}

// demoRestrictedStackDetails is the structured denial envelope for stack-level
// destructive actions on the demo account (FEATURE-ENTITLEMENT-UX-STANDARD).
func demoRestrictedStackDetails(capability string) map[string]any {
	return map[string]any{
		"phase":             "demo_account_guard",
		"phase_label":       "Demo account restriction",
		"error_code":        "demo_account_restricted",
		"reason_code":       "demo_account",
		"capability":        capability,
		"required_features": []string{},
		"missing_features":  []string{},
		"retryable":         false,
		"user_guidance": map[string]any{
			"title": "Disabled on the demo account",
			"body":  "This is the shared kombify live demo. The demo stack and its anchor servers cannot be destroyed.",
			"next_steps": []string{
				"Try the add-server flow if managed-server capacity is available for this account.",
				"Create your own kombify account for full control.",
			},
		},
	}
}
