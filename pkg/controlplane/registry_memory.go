package controlplane

import (
	"context"
	"sort"
	"sync"
	"time"
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
	return listOldestFirst(&s.mu, s.nodes, cloneNode, nodeAge, func(node Node) bool {
		return node.TenantID == tenantID && node.StackID == stackID
	}), nil
}

func (s *MemoryStore) ListNodesByTenant(_ context.Context, tenantID string) ([]Node, error) {
	return listOldestFirst(&s.mu, s.nodes, cloneNode, nodeAge, func(node Node) bool { return node.TenantID == tenantID }), nil
}

func nodeAge(node Node) (time.Time, string) { return node.CreatedAt, node.ID }

func serviceAge(service Service) (time.Time, string) { return service.CreatedAt, service.ID }

// listOldestFirst copies the matching entries of a store map oldest first, ties
// by id: the order the durable store reads them in.
func listOldestFirst[T any](mu *sync.RWMutex, entries map[string]T, clone func(T) *T, age func(T) (time.Time, string), match func(T) bool) []T {
	mu.RLock()
	defer mu.RUnlock()

	out := make([]T, 0)
	for _, entry := range entries {
		if match(entry) {
			out = append(out, *clone(entry))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		createdI, idI := age(out[i])
		createdJ, idJ := age(out[j])
		if !createdI.Equal(createdJ) {
			return createdI.Before(createdJ)
		}
		return idI < idJ
	})
	return out
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
	return listOldestFirst(&s.mu, s.svcs, cloneService, serviceAge, func(service Service) bool {
		return service.TenantID == tenantID && service.StackID == stackID
	}), nil
}

func (s *MemoryStore) ListServicesByTenant(_ context.Context, tenantID string) ([]Service, error) {
	return listOldestFirst(&s.mu, s.svcs, cloneService, serviceAge, func(service Service) bool { return service.TenantID == tenantID }), nil
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
