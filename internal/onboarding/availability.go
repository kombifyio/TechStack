package onboarding

import (
	"context"
	"time"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
)

// UserGuidance is the explain-don't-hide payload of an unavailable step
// (FEATURE-ENTITLEMENT-UX-STANDARD 1.6; ONBOARDING-JOURNEY-STANDARD §7). It
// is the client's `StepAvailability.user_guidance` verbatim.
type UserGuidance struct {
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	NextSteps []string `json:"next_steps"`
}

// StepAvailability is one step's server-decided reachability. The client
// never computes it and never guesses a reason.
type StepAvailability struct {
	Status       string        `json:"status"`
	ReasonCode   string        `json:"reason_code"`
	UserGuidance *UserGuidance `json:"user_guidance"`
}

// Availability statuses, the FEATURE-ENTITLEMENT-UX-STANDARD 1.6 vocabulary.
// `available` is the absence of an entry, so it needs no constant here.
const (
	AvailabilityDisabled = "disabled"
	AvailabilityBlocked  = "blocked"
	AvailabilityPending  = "pending"

	// ReasonSelfhostByOS marks a step that only exists in the managed lane.
	// Self-hosted operators bring their own servers; the step is absent by
	// policy, and §7 requires that absence be explained rather than hidden.
	ReasonSelfhostByOS = "policy.selfhost_byos"
	// ReasonAvailabilityTimeout marks an entitlement read that did not answer
	// in time. Pending is not a denial: the client retries.
	ReasonAvailabilityTimeout = "availability.timeout"
)

// availabilityTimeout bounds the entitlement chain. Onboarding is a read on
// every page load; it may not inherit the create path's patience.
const availabilityTimeout = 2 * time.Second

// FeatureChecker is the entitlement read onboarding needs — the same contract
// the create path uses (internal/routes/stacks/runtime_config.go).
type FeatureChecker interface {
	IsEnabled(ctx context.Context, featureKey string, userID string) (bool, error)
}

// resolveAvailability decides which steps this principal can actually reach.
// Only cost-bearing steps are ever restricted: a step that spends money must
// carry positive entitlement before the UI invites the user into it, and
// everything else is reachable by definition. Steps that are available are
// omitted; the client's default is `available`.
func resolveAvailability(ctx context.Context, journey Journey, mode config.DeploymentMode, features FeatureChecker, userID string) map[string]StepAvailability {
	out := map[string]StepAvailability{}
	costBearing := make([]Step, 0, len(journey.Steps))
	for _, step := range journey.Steps {
		if step.CostBearing {
			costBearing = append(costBearing, step)
		}
	}
	if len(costBearing) == 0 {
		return out
	}

	if !mode.IsValid() {
		mode = config.ModeSelfHosted
	}
	if !mode.IsSaaS() {
		guidance := &UserGuidance{
			Title: "kombify-managed servers are a Cloud feature",
			Body:  "This installation runs self-hosted, so kombify does not provision servers for it. Connect a server you own and the rest of the journey works the same.",
			NextSteps: []string{
				"Connect a server you already run.",
				"Use kombify Cloud if you would rather kombify ran the server for you.",
			},
		}
		for _, step := range costBearing {
			out[step.ID] = StepAvailability{Status: AvailabilityDisabled, ReasonCode: ReasonSelfhostByOS, UserGuidance: guidance}
		}
		return out
	}

	decision := managedRuntimeAvailability(ctx, features, userID)
	if decision == nil {
		return out
	}
	for _, step := range costBearing {
		out[step.ID] = *decision
	}
	return out
}

// managedRuntimeAvailability delegates every provider decision to the same
// billing-sensitive gate as create/add-server. The provider-neutral onboarding
// card is available when at least one exact provider is entitled.
func managedRuntimeAvailability(ctx context.Context, features FeatureChecker, userID string) *StepAvailability {
	ctx, cancel := context.WithTimeout(ctx, availabilityTimeout)
	defer cancel()
	var denial monthlyruntime.ManagedRuntimeEntitlementDecision
	for _, providerID := range []string{monthlyruntime.ProviderCentron, monthlyruntime.ProviderIONOS} {
		decision := monthlyruntime.EvaluateManagedRuntimeEntitlement(ctx, features, userID, providerID)
		if !decision.Denied {
			return nil
		}
		if denial.ReasonCode == "" {
			denial = decision
		}
	}
	if ctx.Err() != nil {
		return &StepAvailability{Status: AvailabilityPending, ReasonCode: ReasonAvailabilityTimeout}
	}
	return &StepAvailability{
		Status:       AvailabilityBlocked,
		ReasonCode:   denial.ReasonCode,
		UserGuidance: entitlementGuidance(denial.ProviderID, denial.ReasonCode, denial.RequiredFeatures, denial.MissingFeatures),
	}
}

// entitlementGuidance reuses the create path's denial copy so the user reads
// one explanation, not two that drifted apart.
func entitlementGuidance(providerID, reasonCode string, required, missing []string) *UserGuidance {
	details := monthlyruntime.ManagedRuntimeEntitlementDenialDetails(providerID, reasonCode, required, missing)
	raw, ok := details["user_guidance"].(map[string]any)
	if !ok {
		return nil
	}
	guidance := &UserGuidance{NextSteps: []string{}}
	guidance.Title, _ = raw["title"].(string)
	guidance.Body, _ = raw["body"].(string)
	if steps, ok := raw["next_steps"].([]string); ok {
		guidance.NextSteps = append(guidance.NextSteps, steps...)
	}
	return guidance
}
