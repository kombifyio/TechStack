package onboarding

import (
	"context"
	"errors"
	"testing"

	"github.com/kombifyio/techstack/pkg/config"
)

type stubChecker struct {
	enabled bool
	err     error
}

type providerFeatureChecker map[string]bool

func (c providerFeatureChecker) IsEnabled(_ context.Context, featureKey, _ string) (bool, error) {
	return c[featureKey], nil
}

func (s stubChecker) IsEnabled(context.Context, string, string) (bool, error) {
	return s.enabled, s.err
}

func costBearingSteps(t *testing.T, journey Journey) []string {
	t.Helper()
	var ids []string
	for _, step := range journey.Steps {
		if step.CostBearing {
			ids = append(ids, step.ID)
		}
	}
	if len(ids) == 0 {
		t.Fatal("the journey has no cost-bearing step to check availability for")
	}
	return ids
}

// Self-host does not provision servers, so the cost-bearing steps are absent
// by policy — and §7 says an absent step must still explain itself.
func TestSelfHostDisablesCostBearingStepsWithGuidance(t *testing.T) {
	journey := testJourney(t)
	got := resolveAvailability(context.Background(), journey, config.ModeSelfHosted, stubChecker{enabled: true}, "user-1")

	for _, id := range costBearingSteps(t, journey) {
		entry, ok := got[id]
		if !ok || entry.Status != AvailabilityDisabled {
			t.Fatalf("%s should be disabled on self-host: %+v", id, entry)
		}
		if entry.UserGuidance == nil || len(entry.UserGuidance.NextSteps) == 0 {
			t.Fatalf("%s must carry actionable guidance: %+v", id, entry.UserGuidance)
		}
	}
	for _, step := range journey.Steps {
		if !step.CostBearing {
			if _, ok := got[step.ID]; ok {
				t.Fatalf("%s is not cost-bearing and must stay available", step.ID)
			}
		}
	}
}

func TestSaaSBlocksCostBearingStepsWhenTheFeatureIsOff(t *testing.T) {
	journey := testJourney(t)
	got := resolveAvailability(context.Background(), journey, config.ModeSaaS, stubChecker{enabled: false}, "user-1")

	for _, id := range costBearingSteps(t, journey) {
		entry, ok := got[id]
		if !ok || entry.Status != AvailabilityBlocked {
			t.Fatalf("%s should be blocked without the entitlement: %+v", id, entry)
		}
		if entry.UserGuidance == nil || entry.UserGuidance.Body == "" {
			t.Fatalf("%s must explain the denial: %+v", id, entry.UserGuidance)
		}
	}
}

func TestEntitledSaaSAccountLeavesEveryStepAvailable(t *testing.T) {
	journey := testJourney(t)
	got := resolveAvailability(context.Background(), journey, config.ModeSaaS, stubChecker{enabled: true}, "user-1")
	if len(got) != 0 {
		t.Fatalf("an entitled account needs no availability entries: %+v", got)
	}
}

func TestIONOSOnlyEntitlementKeepsProviderNeutralOnboardingAvailable(t *testing.T) {
	journey := testJourney(t)
	features := providerFeatureChecker{
		"techstack.managed.runtime":          true,
		"techstack.managed.runtime.cloudkit": true,
		"techstack.managed.runtime.ionos":    true,
	}
	got := resolveAvailability(context.Background(), journey, config.ModeSaaS, features, "user-1")
	if len(got) != 0 {
		t.Fatalf("IONOS-entitled account was blocked by centron availability: %+v", got)
	}
}

// A checker that fails is not a denial the user can act on, so it must not be
// reported with the same reason as a missing entitlement.
func TestCheckerFailureIsDistinguishedFromADisabledFeature(t *testing.T) {
	journey := testJourney(t)
	failing := resolveAvailability(context.Background(), journey, config.ModeSaaS, stubChecker{err: errors.New("upstream down")}, "user-1")
	disabled := resolveAvailability(context.Background(), journey, config.ModeSaaS, stubChecker{enabled: false}, "user-1")

	id := costBearingSteps(t, journey)[0]
	if failing[id].ReasonCode == disabled[id].ReasonCode {
		t.Fatalf("a broken checker and a disabled feature share reason %q", failing[id].ReasonCode)
	}
}
