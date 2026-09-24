package main

import (
	"context"
	"fmt"

	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/middleware"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
)

type providerControlCapacityPolicy struct {
	policy monthlyruntime.CapacityPolicyResolver
}

func newProviderControlCapacityPolicy(edition config.Edition) (providercontrol.CapacityPolicyResolver, error) {
	var authority monthlyruntime.CapacityAuthority
	var entitlements monthlyruntime.CommercialEntitlementResolver
	switch edition {
	case config.EditionSelfHostOSS:
		authority = monthlyruntime.CapacityAuthoritySelfHostOSS
	case config.EditionPreview, config.EditionSaaSStandalone, config.EditionSaaSEmbedded:
		authority = monthlyruntime.CapacityAuthoritySignedEdge
		entitlements = func(ctx context.Context, entitlement string) (bool, error) {
			grants, ok := middleware.SignedEntitlementsFromContext(ctx)
			if !ok {
				return false, fmt.Errorf("verified v2 commercial entitlement envelope is missing")
			}
			return grants.Has(entitlement), nil
		}
	default:
		return nil, fmt.Errorf("compose managed runtime capacity policy: unsupported edition %q", edition)
	}
	policy, err := monthlyruntime.NewManagedRuntimeCapacityPolicy(authority, entitlements)
	if err != nil {
		return nil, fmt.Errorf("compose managed runtime capacity policy: %w", err)
	}
	return providerControlCapacityPolicy{policy: policy}, nil
}

func (p providerControlCapacityPolicy) ResolveCapacity(
	ctx context.Context,
	request providercontrol.CapacityPolicyRequest,
) (providercontrol.CapacityGrant, error) {
	if p.policy == nil {
		return providercontrol.CapacityGrant{}, monthlyruntime.ErrCapacityPolicyInvalidConfiguration
	}
	grant, err := p.policy.ResolveCapacity(ctx, monthlyruntime.CapacityPolicyRequest{
		TenantID: request.TenantID, OwnerSubjectID: request.OwnerSubjectID, ProviderID: request.ProviderID,
	})
	if err != nil {
		return providercontrol.CapacityGrant{}, err
	}
	return providercontrol.CapacityGrant{
		ScopeKind: grant.ScopeKind, ScopeID: grant.ScopeID,
		Mode: providercontrol.CapacityMode(grant.Mode), Limit: grant.Limit,
		DecisionSource: grant.DecisionSource,
	}, nil
}

var _ providercontrol.CapacityPolicyResolver = providerControlCapacityPolicy{}
