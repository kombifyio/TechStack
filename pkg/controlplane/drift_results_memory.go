package controlplane

import (
	"context"
	"sort"
	"strings"
)

func (s *MemoryStore) CreateDriftResult(_ context.Context, result DriftResult) (*DriftResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	result.TenantID = strings.TrimSpace(result.TenantID)
	result.OwnerSubjectID = strings.TrimSpace(result.OwnerSubjectID)
	result.ID = strings.TrimSpace(result.ID)
	result.StackID = strings.TrimSpace(result.StackID)
	if result.ID == "" || result.TenantID == "" || result.OwnerSubjectID == "" || result.StackID == "" {
		return nil, ErrNotFound
	}
	stack, ok := s.stacks[result.StackID]
	if !ok || stack.DeletedAt != nil || stack.TenantID != result.TenantID || stack.OwnerSubjectID != result.OwnerSubjectID {
		return nil, ErrNotFound
	}
	if result.JobID != "" {
		job, ok := s.jobs[result.JobID]
		if !ok || job.TenantID != result.TenantID || job.StackID != result.StackID {
			return nil, ErrNotFound
		}
	}
	if _, exists := s.driftResults[result.ID]; exists {
		return nil, ErrConflict
	}
	result.InstanceID = firstNonEmpty(result.InstanceID, stack.InstanceID)
	result.StackName = stack.Name
	result.CreatedAt = s.now()
	s.driftResults[result.ID] = result
	return cloneDriftResult(result), nil
}

func (s *MemoryStore) GetDriftResult(_ context.Context, tenantID, ownerSubjectID, resultID string) (*DriftResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result, ok := s.driftResults[resultID]
	if !ok || !s.ownsDriftResultLocked(result, tenantID, ownerSubjectID) {
		return nil, ErrNotFound
	}
	return cloneDriftResult(result), nil
}

func (s *MemoryStore) ListDriftResults(_ context.Context, tenantID, ownerSubjectID, stackID string, limit, offset int) ([]DriftResult, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	results := make([]DriftResult, 0)
	for _, result := range s.driftResults {
		if stackID != "" && result.StackID != stackID {
			continue
		}
		if s.ownsDriftResultLocked(result, tenantID, ownerSubjectID) {
			results = append(results, *cloneDriftResult(result))
		}
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].CreatedAt.Equal(results[j].CreatedAt) {
			return results[i].ID > results[j].ID
		}
		return results[i].CreatedAt.After(results[j].CreatedAt)
	})
	total := len(results)
	limit, offset = normalizeDriftResultPage(limit, offset)
	if offset >= total {
		return []DriftResult{}, total, nil
	}
	end := min(offset+limit, total)
	return results[offset:end], total, nil
}

func (s *MemoryStore) DeleteDriftResult(_ context.Context, tenantID, ownerSubjectID, resultID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	result, ok := s.driftResults[resultID]
	if !ok || !s.ownsDriftResultLocked(result, tenantID, ownerSubjectID) {
		return ErrNotFound
	}
	delete(s.driftResults, resultID)
	return nil
}

func (s *MemoryStore) ownsDriftResultLocked(result DriftResult, tenantID, ownerSubjectID string) bool {
	stack, ok := s.stacks[result.StackID]
	return ok && stack.DeletedAt == nil && stack.TenantID == strings.TrimSpace(tenantID) && stack.OwnerSubjectID == strings.TrimSpace(ownerSubjectID)
}

func cloneDriftResult(result DriftResult) *DriftResult {
	result.AffectedResources = cloneSliceOfMaps(result.AffectedResources)
	result.PlanSummary = cloneMap(result.PlanSummary)
	result.Details = cloneMap(result.Details)
	return &result
}

func normalizeDriftResultPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

var _ DriftResultStore = (*MemoryStore)(nil)
