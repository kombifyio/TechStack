package controlplane

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/outcome"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

func (s *MemoryStore) UpsertServerRuntime(_ context.Context, server ServerRuntime) (*ServerRuntime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(server.ID) == "" || strings.TrimSpace(server.TenantID) == "" {
		return nil, ErrNotFound
	}
	now := s.now()
	targetOmitted := !serverregistry.RuntimeTargetIntentPresent(server.RuntimeTarget)
	if existing, ok := s.servers[server.ID]; ok {
		if existing.TenantID != server.TenantID {
			return nil, ErrConflict
		}
		server.CreatedAt = existing.CreatedAt
		if server.LifecycleReasonCode == "" {
			server.LifecycleReasonCode = existing.LifecycleReasonCode
		}
		if server.DesiredReasonCode == "" {
			server.DesiredReasonCode = existing.DesiredReasonCode
		}
		if server.ConnectionReasonCode == "" {
			server.ConnectionReasonCode = existing.ConnectionReasonCode
		}
		if server.HealthReasonCode == "" {
			server.HealthReasonCode = existing.HealthReasonCode
		}
		if server.LifecycleChangedAt.IsZero() {
			server.LifecycleChangedAt = existing.LifecycleChangedAt
		}
		if server.DesiredChangedAt.IsZero() {
			server.DesiredChangedAt = existing.DesiredChangedAt
		}
		if server.ConnectionChangedAt.IsZero() {
			server.ConnectionChangedAt = existing.ConnectionChangedAt
		}
		if server.HealthChangedAt.IsZero() {
			server.HealthChangedAt = existing.HealthChangedAt
		}
		server.Revision = existing.Revision + 1
		if server.Generation <= 0 {
			server.Generation = existing.Generation
		}
		if server.SourceEpoch == "" {
			server.SourceAuthority = existing.SourceAuthority
			server.SourceID = existing.SourceID
			server.SourceEpoch = existing.SourceEpoch
			server.SourceSequence = existing.SourceSequence
			server.SourceObservedAt = cloneTime(existing.SourceObservedAt)
		}
		if targetOmitted {
			server.RuntimeTarget = serverregistry.CloneRuntimeTarget(existing.RuntimeTarget)
		}
		if server.LastOutcome == nil && server.OutcomeChangedAt == nil {
			server.LastOutcome = outcome.Clone(existing.LastOutcome)
			server.OutcomeChangedAt = cloneTime(existing.OutcomeChangedAt)
		}
	} else {
		server.CreatedAt = now
		server.Revision = 1
	}
	serverRuntimeDefaults(&server, now)
	if err := normalizeServerRuntimeOutcome(&server, now); err != nil {
		return nil, err
	}
	if err := serverregistry.ValidateRuntimeTarget(server.RuntimeTarget, server.LeaseID); err != nil {
		return nil, fmt.Errorf("controlplane: invalid server runtime target: %w", err)
	}
	server.UpdatedAt = now
	s.servers[server.ID] = server
	return cloneServerRuntime(server), nil
}

func (s *MemoryStore) EnsureServerRuntimeProjection(_ context.Context, server ServerRuntime) (*ServerRuntime, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(server.ID) == "" || strings.TrimSpace(server.TenantID) == "" {
		return nil, false, ErrNotFound
	}
	if existing, ok := s.servers[server.ID]; ok {
		if existing.TenantID != server.TenantID {
			return nil, false, ErrConflict
		}
		return cloneServerRuntime(existing), false, nil
	}
	now := s.now()
	serverRuntimeDefaults(&server, now)
	if err := normalizeServerRuntimeOutcome(&server, now); err != nil {
		return nil, false, err
	}
	if err := serverregistry.ValidateRuntimeTarget(server.RuntimeTarget, server.LeaseID); err != nil {
		return nil, false, fmt.Errorf("controlplane: invalid server runtime target: %w", err)
	}
	server.CreatedAt = now
	server.UpdatedAt = now
	s.servers[server.ID] = server
	return cloneServerRuntime(server), true, nil
}

func (s *MemoryStore) GetServerRuntime(_ context.Context, tenantID, serverID string) (*ServerRuntime, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	server, ok := s.servers[serverID]
	if !ok || server.TenantID != tenantID {
		return nil, ErrNotFound
	}
	return cloneServerRuntime(server), nil
}

func (s *MemoryStore) ListServerRuntimesByTenant(_ context.Context, tenantID, stackID string) ([]ServerRuntime, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ServerRuntime, 0)
	for _, server := range s.servers {
		if server.TenantID != tenantID || (stackID != "" && server.StackID != stackID) {
			continue
		}
		out = append(out, *cloneServerRuntime(server))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

func (s *MemoryStore) AppendServerTransition(_ context.Context, transition ServerStateTransition) (*ServerStateTransition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	server, ok := s.servers[transition.ServerID]
	if !ok || server.TenantID != transition.TenantID {
		return nil, ErrNotFound
	}
	s.nextServerEventID++
	transition.ID = s.nextServerEventID
	if transition.ObservedAt.IsZero() {
		transition.ObservedAt = s.now()
	}
	transition.CreatedAt = s.now()
	transition.Evidence = cloneMap(transition.Evidence)
	s.serverTransitions[transition.ServerID] = append(s.serverTransitions[transition.ServerID], transition)
	copy := transition
	copy.Evidence = cloneMap(transition.Evidence)
	return &copy, nil
}

func (s *MemoryStore) ListServerTransitions(_ context.Context, tenantID, serverID string, limit int) ([]ServerStateTransition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	server, ok := s.servers[serverID]
	if !ok || server.TenantID != tenantID {
		return nil, ErrNotFound
	}
	rows := s.serverTransitions[serverID]
	if limit <= 0 {
		limit = 100
	}
	out := make([]ServerStateTransition, 0, len(rows))
	for i := len(rows) - 1; i >= 0 && len(out) < limit; i-- {
		item := rows[i]
		item.Evidence = cloneMap(item.Evidence)
		out = append(out, item)
	}
	return out, nil
}

// ListServerTransitionsSince mirrors the Postgres window query so the
// availability projection can be exercised without a database.
func (s *MemoryStore) ListServerTransitionsSince(
	_ context.Context,
	tenantID, dimension string,
	since time.Time,
	limit int,
) ([]ServerStateTransition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 20000 {
		limit = 20000
	}
	out := make([]ServerStateTransition, 0, 16)
	for serverID, rows := range s.serverTransitions {
		server, ok := s.servers[serverID]
		if !ok || server.TenantID != tenantID {
			continue
		}
		for _, row := range rows {
			if !strings.EqualFold(row.Dimension, dimension) || row.ObservedAt.Before(since.UTC()) {
				continue
			}
			item := row
			item.Evidence = cloneMap(item.Evidence)
			out = append(out, item)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ObservedAt.Equal(out[j].ObservedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].ObservedAt.Before(out[j].ObservedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) RecordServerInventory(_ context.Context, snapshot ServerInventorySnapshot) (*ServerInventorySnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	server, ok := s.servers[snapshot.ServerID]
	if !ok || server.TenantID != snapshot.TenantID {
		return nil, ErrNotFound
	}
	if snapshot.Revision <= server.InventoryRevision {
		return nil, ErrConflict
	}
	s.nextServerEventID++
	snapshot.ID = s.nextServerEventID
	if snapshot.ObservedAt.IsZero() {
		snapshot.ObservedAt = s.now()
	}
	snapshot.CreatedAt = s.now()
	snapshot.Inventory = cloneMap(snapshot.Inventory)
	s.serverInventory[snapshot.ServerID] = append(s.serverInventory[snapshot.ServerID], snapshot)
	server.InventoryRevision = snapshot.Revision
	server.Revision++
	server.UpdatedAt = s.now()
	s.servers[server.ID] = server
	copy := snapshot
	copy.Inventory = cloneMap(snapshot.Inventory)
	return &copy, nil
}

var _ ServerRuntimeStore = (*MemoryStore)(nil)
