package controlplane

import (
	"context"
	"fmt"
	"strings"
)

func onboardingKey(tenantID, ownerSubjectID, product, journeyID string) string {
	return tenantID + "\x00" + ownerSubjectID + "\x00" + product + "\x00" + journeyID
}

func (s *MemoryStore) GetOnboardingState(_ context.Context, tenantID, ownerSubjectID, product, journeyID string) (*OnboardingState, error) {
	tenantID = strings.TrimSpace(tenantID)
	ownerSubjectID = strings.TrimSpace(ownerSubjectID)
	product = strings.TrimSpace(product)
	journeyID = strings.TrimSpace(journeyID)
	if tenantID == "" || ownerSubjectID == "" || product == "" || journeyID == "" {
		return nil, fmt.Errorf("controlplane: tenant, owner, product and journey are required")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	state, ok := s.onboarding[onboardingKey(tenantID, ownerSubjectID, product, journeyID)]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneOnboardingState(state), nil
}

func (s *MemoryStore) UpsertOnboardingState(_ context.Context, state OnboardingState, expectRevision *int) (*OnboardingState, error) {
	if err := validateOnboardingState(state); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	normalized := state
	normalized.ID = strings.TrimSpace(state.ID)
	normalized.TenantID = strings.TrimSpace(state.TenantID)
	normalized.OwnerSubjectID = strings.TrimSpace(state.OwnerSubjectID)
	normalized.Product = strings.TrimSpace(state.Product)
	normalized.JourneyID = strings.TrimSpace(state.JourneyID)
	normalized.State = deepCloneIntent(state.State)

	key := onboardingKey(normalized.TenantID, normalized.OwnerSubjectID, normalized.Product, normalized.JourneyID)
	now := s.now()
	existing, ok := s.onboarding[key]
	if expectRevision != nil {
		if !ok {
			return nil, fmt.Errorf("%w: onboarding state does not exist", ErrConflict)
		}
		if existing.Revision != *expectRevision {
			return nil, fmt.Errorf("%w: onboarding state revision moved", ErrConflict)
		}
	}
	if ok {
		normalized.ID = existing.ID
		normalized.CreatedAt = existing.CreatedAt
	} else {
		normalized.CreatedAt = now
	}
	normalized.UpdatedAt = now
	s.onboarding[key] = normalized
	return cloneOnboardingState(normalized), nil
}

var _ OnboardingStateStore = (*MemoryStore)(nil)
