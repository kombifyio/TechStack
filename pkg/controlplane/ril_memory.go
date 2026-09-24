package controlplane

import (
	"context"
	"sort"
)

func (s *MemoryStore) UpsertRILServer(_ context.Context, server RILServer) (*RILServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if existing, ok := s.rilSrv[server.ID]; ok {
		server.CreatedAt = existing.CreatedAt
	} else {
		server.CreatedAt = now
	}
	if server.Status == "" {
		server.Status = "unknown"
	}
	server.UpdatedAt = now
	server.Health = cloneMap(server.Health)
	server.Inventory = cloneMap(server.Inventory)
	server.LastSeenAt = cloneTime(server.LastSeenAt)
	s.rilSrv[server.ID] = server
	return cloneRILServer(server), nil
}

func (s *MemoryStore) ListRILServersByTenant(_ context.Context, tenantID string) ([]RILServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]RILServer, 0)
	for _, server := range s.rilSrv {
		if server.TenantID != tenantID {
			continue
		}
		out = append(out, *cloneRILServer(server))
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := out[i].LastSeenAt, out[j].LastSeenAt
		switch {
		case left != nil && right != nil && !left.Equal(*right):
			return left.After(*right)
		case (left != nil) != (right != nil):
			return left != nil
		default:
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
	})
	return out, nil
}

func (s *MemoryStore) GetRILServer(_ context.Context, tenantID, serverID string) (*RILServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if server, ok := s.rilSrv[serverID]; ok && server.TenantID == tenantID {
		return cloneRILServer(server), nil
	}
	for _, server := range s.rilSrv {
		if server.TenantID == tenantID && server.NodeID == serverID {
			return cloneRILServer(server), nil
		}
	}
	return nil, ErrNotFound
}

func (s *MemoryStore) GetRILCommand(_ context.Context, tenantID, commandID string) (*RILCommand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	command, ok := s.rilCmd[commandID]
	if !ok || command.TenantID != tenantID {
		return nil, ErrNotFound
	}
	return cloneRILCommand(command), nil
}

func (s *MemoryStore) EnqueueRILCommand(_ context.Context, command RILCommand) (*RILCommand, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if existing, ok := s.rilCmd[command.ID]; ok {
		command.CreatedAt = existing.CreatedAt
	} else {
		command.CreatedAt = now
	}
	if command.Status == "" {
		command.Status = "queued"
	}
	command.UpdatedAt = now
	command.Request = cloneMap(command.Request)
	command.Result = cloneMap(command.Result)
	command.CompletedAt = cloneTime(command.CompletedAt)
	s.rilCmd[command.ID] = command
	return cloneRILCommand(command), nil
}

func (s *MemoryStore) UpsertActionCard(_ context.Context, card RILActionCard) (*RILActionCard, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if existing, ok := s.rilCrd[card.ID]; ok {
		card.CreatedAt = existing.CreatedAt
	} else {
		card.CreatedAt = now
	}
	if card.Status == "" {
		card.Status = "open"
	}
	if card.Severity == "" {
		card.Severity = "info"
	}
	card.UpdatedAt = now
	card.Action = cloneMap(card.Action)
	card.Decision = cloneMap(card.Decision)
	card.ResolvedAt = cloneTime(card.ResolvedAt)
	s.rilCrd[card.ID] = card
	return cloneRILActionCard(card), nil
}

func (s *MemoryStore) RecordHealEvent(_ context.Context, event RILHealEvent) (*RILHealEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if existing, ok := s.rilEvt[event.ID]; ok {
		event.CreatedAt = existing.CreatedAt
	} else {
		event.CreatedAt = now
	}
	event.UpdatedAt = now
	event.Details = cloneMap(event.Details)
	s.rilEvt[event.ID] = event
	return cloneRILHealEvent(event), nil
}

func (s *MemoryStore) ListHealEvents(_ context.Context, tenantID, serverID string) ([]RILHealEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]RILHealEvent, 0)
	for _, event := range s.rilEvt {
		if event.TenantID == tenantID && (serverID == "" || event.ServerID == serverID) {
			out = append(out, *cloneRILHealEvent(event))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

var _ RILStore = (*MemoryStore)(nil)
