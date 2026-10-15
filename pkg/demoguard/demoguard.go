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
	// MetadataKeySlot is the lease metadata key that classifies a demo-tenant
	// lease. The nightly reset works from an allowlist: only a lease whose slot
	// is SlotResettable can be decommissioned by it. Every other lease, marked
	// with another slot or not marked at all, is left alone.
	MetadataKeySlot = "demo_slot"
	// SlotResettable marks the disposable demo slot the nightly reset may
	// decommission.
	SlotResettable = "resettable"
	// SlotShowcase marks a persistent demo server that visitors see. The reset
	// never touches it, and the reset workflow fails if one is ever admitted.
	SlotShowcase = "showcase"
	// SlotUnmarked is the receipt label of a lease without a slot mark.
	SlotUnmarked = "unmarked"
)

// LeaseSlot returns the normalized demo slot of a lease's metadata, or
// SlotUnmarked when the lease carries no mark.
func LeaseSlot(metadata map[string]string) string {
	slot := strings.ToLower(strings.TrimSpace(metadata[MetadataKeySlot]))
	if slot == "" {
		return SlotUnmarked
	}
	return slot
}

// IsResettableSlot reports whether the metadata explicitly marks a lease as
// the resettable demo slot. It is the only positive answer the reset accepts.
func IsResettableSlot(metadata map[string]string) bool {
	return LeaseSlot(metadata) == SlotResettable
}

// DemoTenantID returns the configured demo tenant id, or "" when unset.
func DemoTenantID() string {
	return strings.TrimSpace(os.Getenv(EnvDemoTenantID))
}

// IsDemoTenant reports whether tenantID is the configured demo tenant.
func IsDemoTenant(tenantID string) bool {
	demo := DemoTenantID()
	return demo != "" && strings.TrimSpace(tenantID) == demo
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
