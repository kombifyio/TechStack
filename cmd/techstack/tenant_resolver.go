package main

import (
	"context"
	"sort"
	"strings"

	commonauthflow "github.com/kombifyio/go-common/authflow"
	"github.com/kombifyio/go-common/oidcclient"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/demoguard"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/logger"
)

// orgTenantPrefix marks control-plane tenants that mirror an Auth0
// organization id (the portal stamps these into app_metadata.org_id).
const orgTenantPrefix = "org_"

// personalTenantPrefix is the cross-product owner-tenant namespace minted by
// kombify-Gateway for identities without an organization claim.
const personalTenantPrefix = "usr:"

// sharedDefaultTenantID is the legacy shared fallback tenant.
const sharedDefaultTenantID = "default"

// kombifyClaimNamespace prefixes the custom claims minted by the Auth0
// post-login action (kombify-add-roles-to-token); the Cloudflare edge derives
// x-org-id from the same claims.
const kombifyClaimNamespace = "https://kombify.io/"

// v2CloudTenantResolver resolves the tenant stamped into the v2 session at
// cloud-login time so standalone and embedded resolve the same tenant
// (one-truth rule). Chain: demo tenant -> kombify org/tenant claims (the same
// ones the edge turns into x-org-id) -> org_* control-plane membership -> any
// existing active membership -> Auth0 Management API organizations ->
// owner tenant (the subject itself). The final rung mirrors the data-plane
// owner-as-tenant behavior so a user without any provisioned org resolves the
// SAME tenant through the browser session as through the gateway. The
// resolver never fails the login. Installed only in SaaS mode.
func v2CloudTenantResolver(store controlplane.AuthStore, orgs organizationLister, defaultTenant string) commonauthflow.TenantResolver {
	_ = strings.TrimSpace(defaultTenant) // legacy tenants surface via memberships, not as a blanket fallback
	return func(ctx context.Context, claims *oidcclient.Claims, _ string, _ string) (string, error) {
		if claims == nil {
			return sharedDefaultTenantID, nil
		}
		subject := strings.TrimSpace(claims.Subject)
		if subject == "" {
			return sharedDefaultTenantID, nil
		}
		if demoguard.IsDemoUser(subject) {
			if tenantID := demoguard.DemoTenantID(); tenantID != "" {
				return tenantID, nil
			}
		}
		if orgID := rawClaimString(claims,
			kombifyClaimNamespace+"org_id",
			kombifyClaimNamespace+"tenant_id",
			"org_id",
			"org",
		); orgID != "" {
			return orgID, nil
		}
		if membership := preferredMembershipFromStore(ctx, store, subject); membership != nil {
			return strings.TrimSpace(membership.TenantID), nil
		}
		if orgs != nil {
			orgIDs, err := orgs.UserOrganizations(ctx, subject)
			if err != nil {
				logger.Default().Warn("v2_cloud_org_lookup_failed", "error", err, "subject", subject)
			} else if len(orgIDs) > 0 {
				sort.Strings(orgIDs)
				if len(orgIDs) > 1 {
					logger.Default().Warn("org_resolution_ambiguous", "source", "auth0_mgmt", "subject", subject, "count", len(orgIDs))
				}
				return orgIDs[0], nil
			}
		}
		// Owner tenant: the same deterministic fallback the data plane uses
		// for identities without any org context.
		return canonicalOwnerTenantID(subject), nil
	}
}

func canonicalOwnerTenantID(subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return ""
	}
	return personalTenantPrefix + subject
}

func isOwnerTenantID(tenantID, subject string) bool {
	tenantID = strings.TrimSpace(tenantID)
	subject = strings.TrimSpace(subject)
	if tenantID == "" || subject == "" {
		return false
	}
	// The naked subject is the pre-standardization browser-session form. Keep
	// it readable while all new fallbacks converge on the Gateway namespace.
	return tenantID == canonicalOwnerTenantID(subject) || tenantID == subject
}

// preferredMembershipFromStore lists the user's active memberships and picks
// deterministically: org_* tenants win, then the newest membership (list
// order is newest first; ties broken by tenant id).
func preferredMembershipFromStore(ctx context.Context, store controlplane.AuthStore, userID string) *controlplane.Membership {
	if store == nil {
		return nil
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil
	}
	memberships, err := store.ListMembershipsByUser(ctx, userID)
	if err != nil {
		logger.Default().Warn("v2_cloud_membership_lookup_failed", "error", err, "subject", userID)
		return nil
	}
	if len(memberships) == 0 {
		return nil
	}
	seenOrgTenants := map[string]struct{}{}
	for _, membership := range memberships {
		if tenantID := strings.TrimSpace(membership.TenantID); strings.HasPrefix(tenantID, orgTenantPrefix) {
			seenOrgTenants[tenantID] = struct{}{}
		}
	}
	if len(seenOrgTenants) > 1 {
		logger.Default().Warn("org_resolution_ambiguous", "source", "memberships", "subject", userID, "count", len(seenOrgTenants))
	}
	sorted := make([]controlplane.Membership, len(memberships))
	copy(sorted, memberships)
	sort.SliceStable(sorted, func(i, j int) bool {
		iOrg := strings.HasPrefix(strings.TrimSpace(sorted[i].TenantID), orgTenantPrefix)
		jOrg := strings.HasPrefix(strings.TrimSpace(sorted[j].TenantID), orgTenantPrefix)
		if iOrg != jOrg {
			return iOrg
		}
		if sorted[i].UpdatedAt.Equal(sorted[j].UpdatedAt) {
			return sorted[i].TenantID < sorted[j].TenantID
		}
		return sorted[i].UpdatedAt.After(sorted[j].UpdatedAt)
	})
	picked := sorted[0]
	if strings.TrimSpace(picked.TenantID) == "" {
		return nil
	}
	return &picked
}

// preferredOrgMembershipForIdentity is the per-request variant used for
// legacy v2 sessions whose claims carry no tenant. It only promotes org_*
// memberships (the default-tenant chain already covers the rest) and never
// consults the Management API.
func preferredOrgMembershipForIdentity(ctx context.Context, store controlplane.AuthStore, id *identity.Identity) *controlplane.Membership {
	if id == nil || !id.IsAuthenticated() {
		return nil
	}
	membership := preferredMembershipFromStore(ctx, store, id.UserID)
	if membership == nil || !strings.HasPrefix(strings.TrimSpace(membership.TenantID), orgTenantPrefix) {
		return nil
	}
	return membership
}

// rawClaimString reads the first non-empty string claim from the raw ID-token
// claim set.
func rawClaimString(claims *oidcclient.Claims, keys ...string) string {
	if claims == nil || claims.Raw == nil {
		return ""
	}
	for _, key := range keys {
		if value, ok := claims.Raw[key].(string); ok {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
	}
	return ""
}
