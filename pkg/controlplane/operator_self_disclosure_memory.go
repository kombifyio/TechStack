package controlplane

import (
	"context"
	"fmt"
	"strings"
)

func operatorSelfDisclosureKey(tenantID, ownerSubjectID string) string {
	return tenantID + "\x00" + ownerSubjectID
}

func (s *MemoryStore) GetOperatorSelfDisclosure(_ context.Context, tenantID, ownerSubjectID string) (*OperatorSelfDisclosure, error) {
	tenantID = strings.TrimSpace(tenantID)
	ownerSubjectID = strings.TrimSpace(ownerSubjectID)
	if tenantID == "" || ownerSubjectID == "" {
		return nil, fmt.Errorf("controlplane: tenant and owner are required")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	disclosure, ok := s.selfDisclosures[operatorSelfDisclosureKey(tenantID, ownerSubjectID)]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneOperatorSelfDisclosure(disclosure), nil
}

func (s *MemoryStore) UpsertOperatorSelfDisclosure(_ context.Context, disclosure OperatorSelfDisclosure) (*OperatorSelfDisclosure, error) {
	normalized, err := normalizeOperatorSelfDisclosure(disclosure)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := operatorSelfDisclosureKey(normalized.TenantID, normalized.OwnerSubjectID)
	now := s.now()
	if existing, ok := s.selfDisclosures[key]; ok {
		normalized.ID = existing.ID
		normalized.CreatedAt = existing.CreatedAt
	} else {
		normalized.CreatedAt = now
	}
	normalized.UpdatedAt = now
	s.selfDisclosures[key] = normalized
	return cloneOperatorSelfDisclosure(normalized), nil
}

var _ OperatorSelfDisclosureStore = (*MemoryStore)(nil)
