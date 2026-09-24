package monthlyruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/kombifyio/techstack/pkg/identity"
)

type managedRuntimeEntitlementChecker struct {
	enabled bool
	err     error
	calls   int
}

func (c *managedRuntimeEntitlementChecker) IsEnabled(context.Context, string, string) (bool, error) {
	c.calls++
	return c.enabled, c.err
}

// Billing is a registered sensitive boundary: every cost-bearing managed
// provider entry point must share these fail-closed outcomes.
func TestEvaluateManagedRuntimeEntitlement(t *testing.T) {
	t.Setenv("TECHSTACK_ENV", "production")
	t.Setenv("TECHSTACK_ALLOW_LOCAL_SIMULATION_GATE", "")
	t.Setenv("TECHSTACK_ALLOW_LOCAL_MANAGED_RUNTIME_E2E", "")

	checkErr := errors.New("feature service unavailable")
	adminContext := identity.NewContext(context.Background(), &identity.Identity{
		UserID: "admin-1", Roles: []string{"global_admin"},
	})
	tests := []struct {
		name       string
		ctx        context.Context
		providerID string
		checker    *managedRuntimeEntitlementChecker
		wantDenied bool
		wantReason string
		wantNoCall bool
	}{
		{name: "allowed", ctx: context.Background(), providerID: ProviderIONOS, checker: &managedRuntimeEntitlementChecker{enabled: true}},
		{name: "missing checker", ctx: context.Background(), providerID: ProviderCentron, wantDenied: true, wantReason: EntitlementReasonCheckerUnavailable},
		{name: "checker error", ctx: context.Background(), providerID: ProviderCentron, checker: &managedRuntimeEntitlementChecker{err: checkErr}, wantDenied: true, wantReason: EntitlementReasonFeatureCheckFailed},
		{name: "disabled", ctx: context.Background(), providerID: ProviderCentron, checker: &managedRuntimeEntitlementChecker{}, wantDenied: true, wantReason: EntitlementReasonFeatureDisabled},
		{name: "unknown provider", ctx: context.Background(), providerID: "personal-ionos", checker: &managedRuntimeEntitlementChecker{enabled: true}, wantDenied: true, wantReason: EntitlementReasonProviderUnsupported, wantNoCall: true},
		{name: "noncanonical provider", ctx: context.Background(), providerID: "IONOS", checker: &managedRuntimeEntitlementChecker{enabled: true}, wantDenied: true, wantReason: EntitlementReasonProviderUnsupported, wantNoCall: true},
		{name: "platform admin", ctx: adminContext, providerID: ProviderCentron, wantNoCall: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var checker FeatureChecker
			if test.checker != nil {
				checker = test.checker
			}
			decision := EvaluateManagedRuntimeEntitlement(test.ctx, checker, "owner-1", test.providerID)
			if decision.Denied != test.wantDenied || decision.ReasonCode != test.wantReason {
				t.Fatalf("decision = %+v", decision)
			}
			if test.wantNoCall && test.checker != nil && test.checker.calls != 0 {
				t.Fatalf("feature checker was called before the policy decision: %d", test.checker.calls)
			}
			if test.providerID == "personal-ionos" {
				details := decision.Details()
				requiredFeatures, _ := details["required_features"].([]string)
				if details["provider_id"] != "personal-ionos" {
					t.Fatalf("unsupported-provider details = %+v", details)
				}
				for _, featureKey := range requiredFeatures {
					t.Errorf("unsupported provider advertised entitlement %q", featureKey)
				}
			}
		})
	}

	t.Setenv("TECHSTACK_ENV", "development")
	t.Setenv("TECHSTACK_ALLOW_LOCAL_SIMULATION_GATE", "true")
	t.Setenv("TECHSTACK_ALLOW_LOCAL_MANAGED_RUNTIME_E2E", "true")
	if decision := EvaluateManagedRuntimeEntitlement(context.Background(), nil, "owner-1", ProviderCentron); decision.Denied {
		t.Fatalf("explicit local E2E path denied: %+v", decision)
	}
}
