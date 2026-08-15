package controlplane

import (
	"context"
	"fmt"
	"strings"
)

func (s *MemoryStore) GetWizardRunByKey(_ context.Context, tenantID, ownerSubjectID, idempotencyKey string) (*WizardRun, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	ownerSubjectID = strings.TrimSpace(ownerSubjectID)
	if ownerSubjectID == "" {
		return nil, fmt.Errorf("controlplane: owner subject id required")
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return nil, fmt.Errorf("controlplane: idempotency key required")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	run := s.wizardRunByKeyLocked(tenantID, ownerSubjectID, idempotencyKey)
	if run == nil {
		return nil, ErrNotFound
	}
	return cloneWizardRun(*run), nil
}

func (s *MemoryStore) GetLatestWizardRunByOwner(_ context.Context, tenantID, ownerSubjectID string) (*WizardRun, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	ownerSubjectID = strings.TrimSpace(ownerSubjectID)
	if ownerSubjectID == "" {
		return nil, fmt.Errorf("controlplane: owner subject id required")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var latest *WizardRun
	for id := range s.wizardRuns {
		run := s.wizardRuns[id]
		if run.TenantID != tenantID || run.OwnerSubjectID != ownerSubjectID {
			continue
		}
		if latest == nil || run.UpdatedAt.After(latest.UpdatedAt) {
			candidate := run
			latest = &candidate
		}
	}
	if latest == nil {
		return nil, ErrNotFound
	}
	return cloneWizardRun(*latest), nil
}

func (s *MemoryStore) UpsertWizardRun(_ context.Context, run WizardRun) (*WizardRun, error) {
	if err := validateWizardRun(run); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	normalized := run
	normalized.ID = strings.TrimSpace(run.ID)
	normalized.TenantID = strings.TrimSpace(run.TenantID)
	normalized.OwnerSubjectID = strings.TrimSpace(run.OwnerSubjectID)
	normalized.IdempotencyKey = strings.TrimSpace(run.IdempotencyKey)
	normalized.Intent = deepCloneIntent(run.Intent)
	normalized.Result = deepCloneIntent(run.Result)

	now := s.now()
	if normalized.IdempotencyKey != "" {
		if existing := s.wizardRunByKeyLocked(normalized.TenantID, normalized.OwnerSubjectID, normalized.IdempotencyKey); existing != nil {
			normalized.ID = existing.ID
			normalized.CreatedAt = existing.CreatedAt
			normalized.UpdatedAt = now
			s.wizardRuns[normalized.ID] = normalized
			return cloneWizardRun(normalized), nil
		}
	}
	if _, exists := s.wizardRuns[normalized.ID]; exists {
		return nil, ErrConflict
	}
	normalized.CreatedAt = now
	normalized.UpdatedAt = now
	s.wizardRuns[normalized.ID] = normalized
	return cloneWizardRun(normalized), nil
}

func (s *MemoryStore) wizardRunByKeyLocked(tenantID, ownerSubjectID, idempotencyKey string) *WizardRun {
	for id := range s.wizardRuns {
		run := s.wizardRuns[id]
		if run.TenantID == tenantID && run.OwnerSubjectID == ownerSubjectID && run.IdempotencyKey == idempotencyKey {
			return &run
		}
	}
	return nil
}

func cloneWizardRun(run WizardRun) *WizardRun {
	clone := run
	clone.Intent = deepCloneIntent(run.Intent)
	clone.Result = deepCloneIntent(run.Result)
	return &clone
}

var _ WizardRunStore = (*MemoryStore)(nil)
