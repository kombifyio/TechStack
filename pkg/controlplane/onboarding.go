package controlplane

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// OnboardingState is one principal's record for one onboarding journey
// (ONBOARDING-JOURNEY-STANDARD §8). State carries the exact
// onboarding-state.v1 document; JourneyVersion, Status and Revision are
// projections of it kept as columns so the store can compare-and-swap on
// user actions without parsing JSON.
type OnboardingState struct {
	ID             string
	TenantID       string
	OwnerSubjectID string
	Product        string
	JourneyID      string
	JourneyVersion int
	SchemaVersion  int
	Status         string
	Revision       int
	State          map[string]any
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// OnboardingStateStore persists onboarding records per tenant and principal.
//
// UpsertOnboardingState writes the record. With expectRevision nil it creates
// or replaces unconditionally — the path for derived merges, which never bump
// the revision. With expectRevision set it is a compare-and-swap on the
// stored revision and returns ErrConflict when the row has moved on; that is
// how two tabs acting on one record cannot overwrite each other.
type OnboardingStateStore interface {
	GetOnboardingState(ctx context.Context, tenantID, ownerSubjectID, product, journeyID string) (*OnboardingState, error)
	UpsertOnboardingState(ctx context.Context, state OnboardingState, expectRevision *int) (*OnboardingState, error)
}

func validateOnboardingState(state OnboardingState) error {
	if strings.TrimSpace(state.ID) == "" {
		return fmt.Errorf("controlplane: onboarding state id required")
	}
	if strings.TrimSpace(state.TenantID) == "" {
		return fmt.Errorf("controlplane: tenant id required")
	}
	if strings.TrimSpace(state.OwnerSubjectID) == "" {
		return fmt.Errorf("controlplane: owner subject id required")
	}
	if strings.TrimSpace(state.Product) == "" {
		return fmt.Errorf("controlplane: onboarding product required")
	}
	if strings.TrimSpace(state.JourneyID) == "" {
		return fmt.Errorf("controlplane: onboarding journey id required")
	}
	if state.JourneyVersion < 1 {
		return fmt.Errorf("controlplane: onboarding journey version must be >= 1")
	}
	if state.SchemaVersion != 1 {
		return fmt.Errorf("controlplane: onboarding schema version must be 1")
	}
	switch state.Status {
	case "active", "completed", "dismissed":
	default:
		return fmt.Errorf("controlplane: onboarding status %q is not valid", state.Status)
	}
	if state.Revision < 0 {
		return fmt.Errorf("controlplane: onboarding revision must be >= 0")
	}
	if state.State == nil {
		return fmt.Errorf("controlplane: onboarding state document required")
	}
	return nil
}

func cloneOnboardingState(state OnboardingState) *OnboardingState {
	out := state
	out.State = deepCloneIntent(state.State)
	return &out
}
