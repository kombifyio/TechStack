package oidcclient

import (
	"strings"

	"github.com/kombifyio/techstack/internal/gocommon/identity"
	"github.com/kombifyio/techstack/internal/gocommon/role"
)

// IdentityFromClaims projects an OIDC [Claims] onto a
// [identity.Identity]. The mapping is intentionally simple — services that
// need richer mapping (per-tenant role tables, group→role rewrites) should
// post-process the returned value.
//
// Claim sources, in priority order:
//
//   - UserID  ← `sub`
//   - Email   ← `email`
//   - EmailVerified ← boolean `email_verified` (false when absent or non-boolean)
//   - OrgID   ← `org_id` || `org` || `project_id` (raw claim)
//   - Tier    ← `tier` || `plan` (raw claim)
//   - Roles   ← when `role.DefaultRoleClaimNamespace` is present, the role
//     package projects known platform roles from the namespace and flat claims,
//     with its canonical ordering, deduplication and safe default. Presence
//     selects this mode even for an empty or malformed namespaced value.
//     Without the namespace, provider-neutral `roles` (JSON array or
//     comma-separated string) || single `role` retain their original order
//     and arbitrary role names.
//
// Returns nil if claims is nil.
func IdentityFromClaims(c *Claims) *identity.Identity {
	if c == nil {
		return nil
	}
	id := &identity.Identity{
		UserID: strings.TrimSpace(c.Subject),
		Email:  strings.TrimSpace(c.Email),

		EmailVerified: c.EmailVerified && strings.TrimSpace(c.Email) != "",
	}
	id.OrgID = firstStringClaim(c.Raw, "org_id", "org", "project_id")
	id.Tier = firstStringClaim(c.Raw, "tier", "plan")
	id.Roles = rolesFromClaims(c.Raw)
	return id
}

func firstStringClaim(raw map[string]interface{}, keys ...string) string {
	if raw == nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := raw[k]; ok {
			if s, ok := v.(string); ok {
				if t := strings.TrimSpace(s); t != "" {
					return t
				}
			}
		}
	}
	return ""
}

func rolesFromClaims(raw map[string]interface{}) []string {
	if raw == nil {
		return nil
	}
	if _, canonical := raw[role.DefaultRoleClaimNamespace]; canonical {
		platformRoles := role.ExtractAllRolesFromClaims(raw, role.DefaultRoleClaimNamespace)
		out := make([]string, len(platformRoles))
		for index, platformRole := range platformRoles {
			out[index] = string(platformRole)
		}
		return out
	}
	if v, ok := raw["roles"]; ok {
		switch t := v.(type) {
		case []interface{}:
			out := make([]string, 0, len(t))
			for _, x := range t {
				if s, ok := x.(string); ok {
					if r := strings.TrimSpace(s); r != "" {
						out = append(out, r)
					}
				}
			}
			if len(out) > 0 {
				return out
			}
		case string:
			out := []string{}
			for _, r := range strings.Split(t, ",") {
				if s := strings.TrimSpace(r); s != "" {
					out = append(out, s)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	if v, ok := raw["role"]; ok {
		if s, ok := v.(string); ok {
			if r := strings.TrimSpace(s); r != "" {
				return []string{r}
			}
		}
	}
	return nil
}
