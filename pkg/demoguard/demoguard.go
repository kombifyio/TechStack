// Package demoguard resolves the shared public-demo identity from the
// environment. The kombify live demo publishes real login credentials, so the
// demo tenant needs request-time guards (no SSH, no command dispatch, no
// decommission of the anchor servers). All configuration is env-based; when the
// variables are unset every predicate returns false and the guards are inert.
package demoguard

import (
	"encoding/json"
	"os"
	"strings"
)

const (
	// EnvDemoTenantID names the tenant (org) the public demo account belongs to.
	EnvDemoTenantID = "TECHSTACK_DEMO_TENANT_ID"
	// EnvDemoUserIDs is a comma-separated list of subject ids that belong to the
	// public demo account (Cloud subject and/or Auth0 sub forms).
	EnvDemoUserIDs = "TECHSTACK_DEMO_USER_IDS"
	// EnvDemoProtectedLeaseIDs is a comma-separated list of lease ids that must
	// never be decommissioned through user-facing routes. Only dedicated
	// provider-created demo VPS leases belong here; personal development VPS
	// subscriptions are never demo anchors.
	EnvDemoProtectedLeaseIDs = "TECHSTACK_DEMO_PROTECTED_LEASE_IDS"
)

// DemoTenantID returns the configured demo tenant id, or "" when unset.
func DemoTenantID() string {
	return strings.TrimSpace(os.Getenv(EnvDemoTenantID))
}

// IsDemoTenant reports whether tenantID is the configured demo tenant.
func IsDemoTenant(tenantID string) bool {
	demo := DemoTenantID()
	return demo != "" && strings.TrimSpace(tenantID) == demo
}

// DemoOwnerID returns the first configured demo subject id (the owner used when
// the demo automation provisions the anchor stack on the shared demo account),
// falling back to the demo tenant id. Returns "" when nothing is configured.
func DemoOwnerID() string {
	for _, id := range splitList(os.Getenv(EnvDemoUserIDs)) {
		return id
	}
	return DemoTenantID()
}

// IsDemoUser reports whether userID is one of the configured demo subject ids.
func IsDemoUser(userID string) bool {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}
	for _, id := range splitList(os.Getenv(EnvDemoUserIDs)) {
		if id == userID {
			return true
		}
	}
	return false
}

// IsDemoSubject reports whether the request principal belongs to the public
// demo account, by tenant or by subject id.
func IsDemoSubject(tenantID, userID string) bool {
	return IsDemoTenant(tenantID) || IsDemoUser(userID)
}

// IsProtectedLease reports whether leaseID is one of the demo anchor leases.
func IsProtectedLease(leaseID string) bool {
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return false
	}
	for _, id := range splitList(os.Getenv(EnvDemoProtectedLeaseIDs)) {
		if id == leaseID {
			return true
		}
	}
	return false
}

// ProtectedLeaseIDs returns the configured anchor lease ids.
func ProtectedLeaseIDs() []string {
	return splitList(os.Getenv(EnvDemoProtectedLeaseIDs))
}

func splitList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	// Deployment contracts may carry subject lists as JSON (the canonical
	// deployment form) while local/test environments commonly use CSV. Accept
	// both representations so the live demo identity cannot silently miss its
	// managed-runtime entitlement.
	if strings.HasPrefix(raw, "[") || strings.HasPrefix(raw, "\"") {
		var values []string
		if err := json.Unmarshal([]byte(raw), &values); err == nil {
			out := make([]string, 0, len(values))
			for _, value := range values {
				if value = strings.TrimSpace(value); value != "" {
					out = append(out, value)
				}
			}
			return out
		}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
