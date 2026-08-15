package vmleases

import (
	"context"
	"errors"
	"strings"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

// LeaseExecutionAuthority is the immutable execution lane selected before a
// managed lease may cause its first provider side effect. Historical Simulate
// custody and TechStack-native provider control are deliberately disjoint.
type LeaseExecutionAuthority string

const (
	// LeaseExecutionAuthorityLegacySimulate is a non-executable quarantine
	// marker for exact historical provider custody. It never authorizes a call.
	LeaseExecutionAuthorityLegacySimulate LeaseExecutionAuthority = "legacy_simulate"
	// LeaseExecutionAuthorityTechStackProviderControl is the only authority
	// eligible for newly admitted managed-provider operations.
	LeaseExecutionAuthorityTechStackProviderControl LeaseExecutionAuthority = "techstack_provider_control"
)

var (
	ErrLeaseExecutionAuthorityUnbound  = errors.New("vmleases: lease execution authority is unbound")
	ErrLeaseExecutionAuthorityConflict = errors.New("vmleases: lease execution authority conflicts with requested lane")
)

// LeaseExecutionAuthorityReader exposes only an already-persisted authority.
// Selecting or changing a lane is intentionally absent from this interface.
type LeaseExecutionAuthorityReader interface {
	ExecutionAuthority(ctx context.Context, tenantID string, leaseID vmlease.LeaseID) (LeaseExecutionAuthority, error)
}

func normalizeLeaseExecutionAuthority(authority LeaseExecutionAuthority) LeaseExecutionAuthority {
	return LeaseExecutionAuthority(strings.ToLower(strings.TrimSpace(string(authority))))
}

func validateLeaseExecutionAuthority(authority LeaseExecutionAuthority) error {
	switch normalizeLeaseExecutionAuthority(authority) {
	case LeaseExecutionAuthorityLegacySimulate, LeaseExecutionAuthorityTechStackProviderControl:
		return nil
	default:
		return ErrLeaseExecutionAuthorityUnbound
	}
}
