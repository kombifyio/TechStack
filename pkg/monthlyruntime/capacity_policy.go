package monthlyruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kombifyio/go-common/edgeauth"
)

const (
	CapacityScopeKindOwnerSubject = "owner_subject"

	// CapacityBudgetCloudRuntimeCredits is the signed Edge budget whose
	// managed_servers member authorizes the origin hard-accounting ceiling.
	CapacityBudgetCloudRuntimeCredits = "cloud.runtime.credits"
	CapacityBudgetFieldManagedServers = "managed_servers"
	capacityDecisionAudience          = "techstack"
	capacityDecisionPublicPrefix      = "/v1/techstack"

	CapacityDecisionSourceSignedRuntimeBudget = "edge_v2_entitlement+signed_budget:cloud.runtime.credits#managed_servers"
	CapacityDecisionSourceSelfHostManifest    = "static_release_manifest:selfhost-oss"
)

// CapacityAuthority fixes the commercial decision source at construction.
// Request data can never select or weaken this boundary.
type CapacityAuthority string

const (
	CapacityAuthoritySignedEdge  CapacityAuthority = "signed_edge"
	CapacityAuthoritySelfHostOSS CapacityAuthority = "selfhost_oss"
)

// CapacityMode records whether a managed-runtime capacity grant is bounded or
// explicitly unbounded. An empty mode is never a valid grant.
type CapacityMode string

const (
	CapacityModeLimited   CapacityMode = "limited"
	CapacityModeUnlimited CapacityMode = "unlimited"
)

var (
	ErrCapacityPolicyInvalidConfiguration = errors.New("monthlyruntime: invalid capacity policy configuration")
	ErrCapacityPolicyInvalidRequest       = errors.New("monthlyruntime: invalid capacity policy request")
	ErrCapacityPolicyAuthorityUnavailable = errors.New("monthlyruntime: capacity authority unavailable")
)

// CapacityGrant is the immutable input to a durable managed-runtime capacity
// reservation. Unlimited is explicit (Mode=unlimited, Limit=0).
type CapacityGrant struct {
	ScopeKind      string
	ScopeID        string
	Mode           CapacityMode
	Limit          int
	DecisionSource string
}

// CapacityPolicyRequest binds a capacity decision to one tenant, its
// authenticated owner subject, and an exact canonical managed provider.
type CapacityPolicyRequest struct {
	TenantID       string
	OwnerSubjectID string
	ProviderID     string
}

// CapacityPolicyResolver resolves an explicit, owner-scoped capacity grant.
type CapacityPolicyResolver interface {
	ResolveCapacity(ctx context.Context, request CapacityPolicyRequest) (CapacityGrant, error)
}

// CommercialEntitlementResolver reads only grants authenticated by the v2
// Edge identity envelope. Feature-delivery flags and local preferences are not
// valid implementations of this boundary.
type CommercialEntitlementResolver func(context.Context, string) (bool, error)

type managedRuntimeCapacityPolicy struct {
	authority    CapacityAuthority
	entitlements CommercialEntitlementResolver
}

// NewManagedRuntimeCapacityPolicy construction-fixes the authority lane.
// SaaS/preview consume only verified per-request Edge decisions. Self-host OSS
// consumes its static release-manifest policy and has no SaaS dependency.
func NewManagedRuntimeCapacityPolicy(
	authority CapacityAuthority,
	entitlements CommercialEntitlementResolver,
) (CapacityPolicyResolver, error) {
	switch authority {
	case CapacityAuthoritySignedEdge:
		if entitlements == nil {
			return nil, fmt.Errorf("%w: signed commercial entitlement resolver is required", ErrCapacityPolicyInvalidConfiguration)
		}
		return &managedRuntimeCapacityPolicy{authority: authority, entitlements: entitlements}, nil
	case CapacityAuthoritySelfHostOSS:
		return &managedRuntimeCapacityPolicy{authority: authority}, nil
	default:
		return nil, fmt.Errorf("%w: unsupported authority %q", ErrCapacityPolicyInvalidConfiguration, authority)
	}
}

func (p *managedRuntimeCapacityPolicy) ResolveCapacity(ctx context.Context, request CapacityPolicyRequest) (CapacityGrant, error) {
	if p == nil {
		return CapacityGrant{}, ErrCapacityPolicyInvalidConfiguration
	}
	tenantID := strings.TrimSpace(request.TenantID)
	ownerSubjectID := strings.TrimSpace(request.OwnerSubjectID)
	providerID := strings.ToLower(strings.TrimSpace(request.ProviderID))
	if ctx == nil || tenantID == "" || ownerSubjectID == "" {
		return CapacityGrant{}, fmt.Errorf("%w: context, tenant, and owner are required", ErrCapacityPolicyInvalidRequest)
	}
	if providerID != ProviderIONOS && providerID != ProviderCentron {
		return CapacityGrant{}, fmt.Errorf("%w: exact managed provider is required", ErrCapacityPolicyInvalidRequest)
	}

	base := CapacityGrant{ScopeKind: CapacityScopeKindOwnerSubject, ScopeID: ownerSubjectID}
	switch p.authority {
	case CapacityAuthoritySelfHostOSS:
		base.Mode = CapacityModeUnlimited
		base.DecisionSource = CapacityDecisionSourceSelfHostManifest
		return base, nil
	case CapacityAuthoritySignedEdge:
		return signedManagedRuntimeCapacity(ctx, tenantID, providerID, base, p.entitlements)
	default:
		return CapacityGrant{}, ErrCapacityPolicyInvalidConfiguration
	}
}

func signedManagedRuntimeCapacity(
	ctx context.Context,
	tenantID string,
	providerID string,
	grant CapacityGrant,
	entitlements CommercialEntitlementResolver,
) (CapacityGrant, error) {
	decisions, ok := edgeauth.FlagsFromContext(ctx)
	if !ok {
		return CapacityGrant{}, fmt.Errorf("%w: verified edge decision is missing", ErrCapacityPolicyAuthorityUnavailable)
	}
	credits, binding, err := decisions.VerifiedCloudRuntimeCredits()
	if err != nil {
		return CapacityGrant{}, fmt.Errorf("%w: verified runtime-credit budget is invalid: %v", ErrCapacityPolicyAuthorityUnavailable, err)
	}
	if binding.SubjectID != grant.ScopeID || binding.TenantID != tenantID ||
		binding.Audience != capacityDecisionAudience || binding.PublicPrefix != capacityDecisionPublicPrefix {
		return CapacityGrant{}, fmt.Errorf("%w: runtime-credit decision binding does not match the managed-runtime operation", ErrCapacityPolicyAuthorityUnavailable)
	}
	for _, entitlement := range RequiredFeatureKeysForProvider(providerID) {
		granted, err := entitlements(ctx, entitlement)
		if err != nil {
			return CapacityGrant{}, fmt.Errorf("%w: resolve signed entitlement %q: %v", ErrCapacityPolicyAuthorityUnavailable, entitlement, err)
		}
		if !granted {
			return CapacityGrant{}, fmt.Errorf("%w: signed commercial entitlement %q is missing", ErrCapacityPolicyAuthorityUnavailable, entitlement)
		}
	}
	grant.DecisionSource = CapacityDecisionSourceSignedRuntimeBudget
	switch credits.ManagedServers.Mode {
	case edgeauth.ManagedServerCreditModeUnlimited:
		grant.Mode = CapacityModeUnlimited
		return grant, nil
	case edgeauth.ManagedServerCreditModeLimited:
		grant.Mode = CapacityModeLimited
		grant.Limit = credits.ManagedServers.Limit
		return grant, nil
	default:
		return CapacityGrant{}, fmt.Errorf("%w: verified managed-server budget mode is unsupported", ErrCapacityPolicyAuthorityUnavailable)
	}
}
