// Package tenant provides multi-tenancy helpers for SaaS mode.
//
// In SaaS mode (DEPLOYMENT_MODE=saas), Edge injects X-Org-ID from the hosted
// JWT claims. This org_id is used as tenant_id for row-level isolation.
//
// In self-hosted mode, tenant functions are no-ops — all records belong
// to the single instance owner.
package tenant

import (
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/pocketbase/pocketbase/core"
)

// Enforcer provides tenant isolation enforcement for PocketBase operations.
type Enforcer struct {
	mode config.DeploymentMode
}

// NewEnforcer creates a tenant enforcer for the given deployment mode.
func NewEnforcer(mode config.DeploymentMode) *Enforcer {
	return &Enforcer{mode: mode}
}

// IsActive returns true if tenant isolation is enforced (SaaS mode).
func (t *Enforcer) IsActive() bool {
	return t.mode.IsSaaS()
}

// TenantID extracts the tenant ID from a PocketBase request event.
// Returns empty string in self-hosted mode, if the event is nil, or if no
// identity is present on the request. Callers must treat an empty return
// in SaaS mode as a fail-closed signal — see ScopeFilter/ScopeFilterParams.
func (t *Enforcer) TenantID(e *core.RequestEvent) string {
	if !t.IsActive() {
		return ""
	}
	if e == nil || e.Request == nil {
		return ""
	}
	id := identity.FromContext(e.Request.Context())
	if id == nil {
		return ""
	}
	return id.OrgID
}

// denyAllFilter matches zero rows. Used as the fail-closed response when
// SaaS mode is active but no tenant identity is present on the request —
// for example if Edge's X-Org-ID header was stripped or the identity
// middleware misconfigured. Returning "" or "1 = 1" there would expose
// every tenant's data (IDOR).
const denyAllFilter = "0 = 1"

// ScopeFilter returns a PocketBase filter string that limits results to the
// current tenant.
//
// Return contract:
//   - self-hosted mode: "" (caller should run an unfiltered query)
//   - SaaS mode with identity: "tenant_id = '<id>'"
//   - SaaS mode WITHOUT identity: "0 = 1" (fail-closed — never "")
//
// Usage in handlers:
//
//	filter := enforcer.ScopeFilter(e)
//	// filter is safe to append unconditionally; self-hosted returns "".
//	if filter != "" {
//	    records, err := app.FindRecordsByFilter("stacks", filter, ...)
//	}
func (t *Enforcer) ScopeFilter(e *core.RequestEvent) string {
	if !t.IsActive() {
		return ""
	}
	tenantID := t.TenantID(e)
	if tenantID == "" {
		return denyAllFilter
	}
	return "tenant_id = '" + escapePBFilter(tenantID) + "'"
}

// ScopeFilterParams returns a filter string and params map for parameterized
// PocketBase queries. This is the preferred approach over ScopeFilter to
// prevent injection.
//
// Return contract (same fail-closed semantics as ScopeFilter):
//   - self-hosted mode: "1 = 1", nil
//   - SaaS mode with identity: "tenant_id = {:tenant_id}", params
//   - SaaS mode WITHOUT identity: "0 = 1", nil
//
// Usage:
//
//	filter, params := enforcer.ScopeFilterParams(e)
//	records, err := app.FindRecordsByFilter("stacks", filter, "", 0, 0, params)
func (t *Enforcer) ScopeFilterParams(e *core.RequestEvent) (string, map[string]any) {
	if !t.IsActive() {
		return "1 = 1", nil
	}
	tenantID := t.TenantID(e)
	if tenantID == "" {
		return denyAllFilter, nil
	}
	return "tenant_id = {:tenant_id}", map[string]any{
		"tenant_id": tenantID,
	}
}

// StampRecord sets the tenant_id on a new record before saving.
// No-op in self-hosted mode.
func (t *Enforcer) StampRecord(e *core.RequestEvent, record *core.Record) {
	tenantID := t.TenantID(e)
	if tenantID != "" {
		record.Set("tenant_id", tenantID)
	}
}

// VerifyAccess checks that a record belongs to the current tenant.
// Returns true if access is allowed (self-hosted mode always allows).
func (t *Enforcer) VerifyAccess(e *core.RequestEvent, record *core.Record) bool {
	if !t.IsActive() {
		return true
	}
	if record == nil {
		return false
	}
	tenantID := t.TenantID(e)
	if tenantID == "" {
		return false // SaaS mode but no tenant identity — deny
	}
	recordTenant := record.GetString("tenant_id")
	return recordTenant == tenantID
}

// VerifyLegacyTenantlessAccess preserves the old migration-window behavior for
// explicit compatibility call sites only. Default SaaS access checks must use
// VerifyAccess so tenantless records fail closed.
func (t *Enforcer) VerifyLegacyTenantlessAccess(e *core.RequestEvent, record *core.Record) bool {
	if record == nil {
		return !t.IsActive()
	}
	if t.VerifyAccess(e, record) {
		return true
	}
	return t.IsActive() && t.TenantID(e) != "" && record.GetString("tenant_id") == ""
}

// escapePBFilter escapes single quotes in PocketBase filter values.
func escapePBFilter(s string) string {
	result := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			result = append(result, '\'', '\'')
		} else {
			result = append(result, s[i])
		}
	}
	return string(result)
}
