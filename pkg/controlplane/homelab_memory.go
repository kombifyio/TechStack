package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func (s *MemoryStore) CreateHomelab(_ context.Context, req CreateHomelabRequest) (*Homelab, error) {
	if err := validateHomelabRequest(req); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	homelab, created := s.insertHomelabLocked(req)
	if !created {
		return nil, ErrConflict
	}
	return cloneHomelab(homelab), nil
}

func (s *MemoryStore) GetHomelabByOwner(_ context.Context, tenantID, ownerSubjectID string) (*Homelab, error) {
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

	homelab := s.activeHomelabByOwnerLocked(tenantID, ownerSubjectID)
	if homelab == nil {
		return nil, ErrNotFound
	}
	return cloneHomelab(*homelab), nil
}

func (s *MemoryStore) GetOrCreateHomelabForOwner(_ context.Context, req CreateHomelabRequest) (*Homelab, error) {
	if err := validateHomelabRequest(req); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	homelab, created := s.insertHomelabLocked(req)
	if created {
		return cloneHomelab(homelab), nil
	}
	existing := s.activeHomelabByOwnerLocked(strings.TrimSpace(req.TenantID), strings.TrimSpace(req.OwnerSubjectID))
	if existing == nil {
		return nil, fmt.Errorf("%w: homelab id already in use", ErrConflict)
	}
	return cloneHomelab(*existing), nil
}

func (s *MemoryStore) UpdateHomelabIntent(_ context.Context, tenantID, homelabID string, intent map[string]any) (*Homelab, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	homelabID = strings.TrimSpace(homelabID)
	if homelabID == "" {
		return nil, fmt.Errorf("controlplane: homelab id required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	homelab, ok := s.homelabs[homelabID]
	if !ok || homelab.TenantID != tenantID || homelab.DeletedAt != nil {
		return nil, ErrNotFound
	}
	homelab.Intent = deepCloneIntent(intent)
	homelab.UpdatedAt = s.now()
	s.homelabs[homelab.ID] = homelab
	return cloneHomelab(homelab), nil
}

func (s *MemoryStore) UpdateHomelabName(_ context.Context, tenantID, homelabID, name string) (*Homelab, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("controlplane: tenant id required")
	}
	homelabID = strings.TrimSpace(homelabID)
	if homelabID == "" {
		return nil, fmt.Errorf("controlplane: homelab id required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("controlplane: homelab name required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	homelab, ok := s.homelabs[homelabID]
	if !ok || homelab.TenantID != tenantID || homelab.DeletedAt != nil {
		return nil, ErrNotFound
	}
	homelab.Name = name
	homelab.UpdatedAt = s.now()
	namedAt := homelab.UpdatedAt
	homelab.NamedAt = &namedAt
	homelab.Identity.Pending = true
	s.homelabs[homelab.ID] = homelab
	return cloneHomelab(homelab), nil
}

func (s *MemoryStore) UpdateHomelabStackIdentity(_ context.Context, tenantID, homelabID string, write HomelabStackIdentityWrite) (*Homelab, error) {
	tenantID = strings.TrimSpace(tenantID)
	homelabID = strings.TrimSpace(homelabID)
	name := strings.TrimSpace(write.Name)
	if tenantID == "" || homelabID == "" || name == "" {
		return nil, fmt.Errorf("controlplane: tenant, homelab and name required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	homelab, ok := s.homelabs[homelabID]
	if !ok || homelab.TenantID != tenantID || homelab.DeletedAt != nil {
		return nil, ErrNotFound
	}
	editedAt := write.EditedAt
	if editedAt.IsZero() {
		editedAt = s.now()
	}
	homelab.Name = name
	homelab.NamedAt = &editedAt
	homelab.UpdatedAt = s.now()
	homelab.Identity = HomelabStackIdentity{
		Presentation:  clonePresentation(write.Presentation),
		CloudRevision: write.CloudRevision,
		Pending:       write.Pending,
	}
	s.homelabs[homelab.ID] = homelab
	return cloneHomelab(homelab), nil
}

func clonePresentation(presentation *StackIdentityPresentation) *StackIdentityPresentation {
	if presentation == nil {
		return nil
	}
	clone := *presentation
	if presentation.AnimationEnabled != nil {
		enabled := *presentation.AnimationEnabled
		clone.AnimationEnabled = &enabled
	}
	if presentation.GlowColorOverride != nil {
		glow := *presentation.GlowColorOverride
		clone.GlowColorOverride = &glow
	}
	return &clone
}

// insertHomelabLocked mirrors the Postgres insert: it fails softly (created ==
// false) on an id collision or when the active (tenant, owner) singleton
// already exists. Callers must hold s.mu.
func (s *MemoryStore) insertHomelabLocked(req CreateHomelabRequest) (Homelab, bool) {
	id := strings.TrimSpace(req.ID)
	tenantID := strings.TrimSpace(req.TenantID)
	ownerSubjectID := strings.TrimSpace(req.OwnerSubjectID)

	if _, exists := s.homelabs[id]; exists {
		return Homelab{}, false
	}
	if existing := s.activeHomelabByOwnerLocked(tenantID, ownerSubjectID); existing != nil {
		return Homelab{}, false
	}

	now := s.now()
	homelab := Homelab{
		ID:             id,
		TenantID:       tenantID,
		OwnerSubjectID: ownerSubjectID,
		Name:           strings.TrimSpace(req.Name),
		Intent:         deepCloneIntent(req.Intent),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if homelab.Intent == nil {
		homelab.Intent = map[string]any{}
	}
	s.homelabs[homelab.ID] = homelab
	return homelab, true
}

func (s *MemoryStore) activeHomelabByOwnerLocked(tenantID, ownerSubjectID string) *Homelab {
	for id := range s.homelabs {
		homelab := s.homelabs[id]
		if homelab.TenantID != tenantID || homelab.DeletedAt != nil {
			continue
		}
		if homelab.OwnerSubjectID == ownerSubjectID {
			return &homelab
		}
	}
	return nil
}

func cloneHomelab(homelab Homelab) *Homelab {
	clone := homelab
	clone.Intent = deepCloneIntent(homelab.Intent)
	clone.DeletedAt = cloneTime(homelab.DeletedAt)
	clone.NamedAt = cloneTime(homelab.NamedAt)
	clone.Identity.Presentation = clonePresentation(homelab.Identity.Presentation)
	return &clone
}

// deepCloneIntent mirrors the Postgres JSONB round-trip so nested intent
// values (goal lists, nested maps) never alias caller or stored memory —
// cloneMap alone is shallow.
func deepCloneIntent(intent map[string]any) map[string]any {
	if len(intent) == 0 {
		return map[string]any{}
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		return cloneMap(intent)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return cloneMap(intent)
	}
	return out
}

var _ HomelabStore = (*MemoryStore)(nil)
