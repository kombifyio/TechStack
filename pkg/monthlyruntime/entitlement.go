package monthlyruntime

import (
	"context"
	"os"
	"strings"

	"github.com/kombifyio/techstack/internal/providercatalog"
	"github.com/kombifyio/techstack/pkg/identity"
)

// ManagedRuntimeEntitlementDecision is the single account/provider
// availability decision shared by every Techstack managed-create surface.
type ManagedRuntimeEntitlementDecision struct {
	Denied           bool
	Message          string
	ProviderID       string
	ReasonCode       string
	RequiredFeatures []string
	MissingFeatures  []string
}

func (d ManagedRuntimeEntitlementDecision) Details() map[string]any {
	return ManagedRuntimeEntitlementDenialDetails(
		d.ProviderID,
		d.ReasonCode,
		d.RequiredFeatures,
		d.MissingFeatures,
	)
}

// EvaluateManagedRuntimeEntitlement fails closed before persistence, queueing,
// admission, or provider dispatch. The local E2E and authenticated platform
// administrator exceptions are product policy here, not route-specific forks.
func EvaluateManagedRuntimeEntitlement(
	ctx context.Context,
	checker FeatureChecker,
	userID string,
	providerID string,
) ManagedRuntimeEntitlementDecision {
	canonicalProviderID, err := providercatalog.CanonicalProviderID(providerID)
	if err != nil {
		return deniedManagedRuntimeEntitlement(providerID, EntitlementReasonProviderUnsupported, nil, nil)
	}
	if LocalManagedRuntimeE2EAllowed() || managedRuntimeAdministrator(ctx) {
		return ManagedRuntimeEntitlementDecision{ProviderID: canonicalProviderID}
	}
	requiredFeatures := RequiredFeatureKeysForProvider(canonicalProviderID)
	if checker == nil {
		return deniedManagedRuntimeEntitlement(canonicalProviderID, EntitlementReasonCheckerUnavailable, requiredFeatures, nil)
	}
	for _, featureKey := range requiredFeatures {
		enabled, checkErr := checker.IsEnabled(ctx, featureKey, userID)
		if checkErr != nil {
			return deniedManagedRuntimeEntitlement(canonicalProviderID, EntitlementReasonFeatureCheckFailed, requiredFeatures, []string{featureKey})
		}
		if !enabled {
			return deniedManagedRuntimeEntitlement(canonicalProviderID, EntitlementReasonFeatureDisabled, requiredFeatures, []string{featureKey})
		}
	}
	return ManagedRuntimeEntitlementDecision{ProviderID: canonicalProviderID, RequiredFeatures: requiredFeatures}
}

func deniedManagedRuntimeEntitlement(providerID, reasonCode string, requiredFeatures, missingFeatures []string) ManagedRuntimeEntitlementDecision {
	return ManagedRuntimeEntitlementDecision{
		Denied: true, Message: ManagedRuntimeEntitlementDeniedMessage,
		ProviderID: providerID, ReasonCode: reasonCode,
		RequiredFeatures: requiredFeatures, MissingFeatures: missingFeatures,
	}
}

func managedRuntimeAdministrator(ctx context.Context) bool {
	principal := identity.FromContext(ctx)
	if principal == nil || !principal.IsAuthenticated() {
		return false
	}
	return principal.HasRole("admin") || principal.HasRole("super_admin") || principal.HasRole("global_admin")
}

// LocalManagedRuntimeE2EAllowed is the one narrow self-host development
// exception. Both switches are required so one stale environment flag cannot
// authorize a cost-bearing managed provider path.
// LocalManagedRuntimeE2EAllowed opens managed monthly-runtime creation on a
// non-SaaS Core for test lanes only: TECHSTACK_ENV development (the Docker
// deployment-target E2E) or local (the loopback Core of
// scripts/os-compat/managed-lab-lane.py --substrate provider), and both
// explicit test flags. Every other environment keeps the SaaS-only rule.
func LocalManagedRuntimeE2EAllowed() bool {
	environment := strings.ToLower(strings.TrimSpace(os.Getenv("TECHSTACK_ENV")))
	return (environment == "development" || environment == "local") &&
		truthyManagedRuntimeEntitlementEnv("TECHSTACK_ALLOW_LOCAL_SIMULATION_GATE") &&
		truthyManagedRuntimeEntitlementEnv("TECHSTACK_ALLOW_LOCAL_MANAGED_RUNTIME_E2E")
}

func truthyManagedRuntimeEntitlementEnv(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
