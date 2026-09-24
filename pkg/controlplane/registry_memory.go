package controlplane

import (
	"context"
)

func (s *MemoryStore) UpsertNode(_ context.Context, node Node) (*Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if existing, ok := s.nodes[node.ID]; ok {
		node.CreatedAt = existing.CreatedAt
	} else {
		node.CreatedAt = now
	}
	if node.Role == "" {
		node.Role = "foundation"
	}
	if node.Status == "" {
		node.Status = "pending"
	}
	node.UpdatedAt = now
	node.Metadata = cloneMap(node.Metadata)
	s.nodes[node.ID] = node
	return cloneNode(node), nil
}

func (s *MemoryStore) UpsertService(_ context.Context, service Service) (*Service, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if existing, ok := s.svcs[service.ID]; ok {
		service.CreatedAt = existing.CreatedAt
	} else {
		service.CreatedAt = now
	}
	// Provenance and ownership are resolved by the same canonical rule the
	// Postgres store and the 074 backfill use, so the memory adapter can never
	// disagree about who owns a service.
	service = resolvedLegacyServiceOwnership(service)
	service.UpdatedAt = now
	service.Metadata = cloneMap(service.Metadata)
	s.svcs[service.ID] = service
	return cloneService(service), nil
}

func (s *MemoryStore) ListNodesByStack(_ context.Context, tenantID, stackID string) ([]Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Node, 0)
	for _, node := range s.nodes {
		if node.TenantID == tenantID && node.StackID == stackID {
			out = append(out, *cloneNode(node))
		}
	}
	return out, nil
}

func (s *MemoryStore) GetNode(_ context.Context, tenantID, nodeID string) (*Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	node, ok := s.nodes[nodeID]
	if !ok || node.TenantID != tenantID {
		return nil, ErrNotFound
	}
	return cloneNode(node), nil
}

func (s *MemoryStore) GetService(_ context.Context, tenantID, serviceID string) (*Service, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	service, ok := s.svcs[serviceID]
	if !ok || service.TenantID != tenantID {
		return nil, ErrNotFound
	}
	return cloneService(service), nil
}

func (s *MemoryStore) ListServicesByStack(_ context.Context, tenantID, stackID string) ([]Service, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Service, 0)
	for _, service := range s.svcs {
		if service.TenantID == tenantID && service.StackID == stackID {
			out = append(out, *cloneService(service))
		}
	}
	return out, nil
}

func (s *MemoryStore) DeleteService(_ context.Context, tenantID, serviceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	service, ok := s.svcs[serviceID]
	if !ok || service.TenantID != tenantID {
		return ErrNotFound
	}
	delete(s.svcs, serviceID)
	delete(s.serviceRuntime, serviceID)
	return nil
}

var _ RegistryStore = (*MemoryStore)(nil)
