package controlplane

import (
	"context"
	"fmt"
	"strings"
)

// ApplyServerEvent commits the aggregate head, transition timeline, and
// optional inventory snapshot while holding the store's single write lock.
func (s *MemoryStore) ApplyServerEvent(_ context.Context, event ServerEvent) (*ServerEventResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyServerEventLocked(event)
}

func (s *MemoryStore) applyServerEventLocked(event ServerEvent) (*ServerEventResult, error) {
	var current *ServerRuntime
	if stored, ok := s.servers[event.ServerID]; ok {
		if stored.TenantID != event.TenantID {
			return nil, ErrConflict
		}
		current = cloneServerRuntime(stored)
	}
	now := s.now()
	epochKey := serverGuardEpochKey(event)
	_, sourceEpochSeen := s.serverGuardEpochs[epochKey]
	prepared, err := prepareServerEvent(current, event, now, sourceEpochSeen)
	if err != nil {
		return nil, err
	}
	if !prepared.applied {
		return &ServerEventResult{Server: cloneServerRuntime(prepared.server), Applied: false}, nil
	}

	prepared.server.CreatedAt = now
	if current != nil {
		prepared.server.CreatedAt = current.CreatedAt
	}
	prepared.server.UpdatedAt = now
	s.servers[event.ServerID] = *cloneServerRuntime(prepared.server)

	transitions := make([]ServerStateTransition, 0, len(prepared.transitions))
	for _, transition := range prepared.transitions {
		s.nextServerEventID++
		transition.ID = s.nextServerEventID
		transition.CreatedAt = now
		transition.Evidence = cloneMap(transition.Evidence)
		s.serverTransitions[event.ServerID] = append(s.serverTransitions[event.ServerID], transition)
		transitions = append(transitions, transition)
	}

	var inventory *ServerInventorySnapshot
	if prepared.inventory != nil {
		s.nextServerEventID++
		item := *prepared.inventory
		item.ID = s.nextServerEventID
		item.CreatedAt = now
		item.Inventory = cloneMap(item.Inventory)
		s.serverInventory[event.ServerID] = append(s.serverInventory[event.ServerID], item)
		copy := item
		copy.Inventory = cloneMap(item.Inventory)
		inventory = &copy
	}

	s.nextServerEventID++
	outbox := *prepared.outbox
	outbox.ID = s.nextServerEventID
	outbox.CreatedAt = now
	outbox.Payload = cloneMap(outbox.Payload)
	s.serverOutbox = append(s.serverOutbox, outbox)
	outboxCopy := outbox
	outboxCopy.Payload = cloneMap(outbox.Payload)
	if event.Authority == ServerEventAuthorityGuard {
		s.serverGuardEpochs[epochKey] = struct{}{}
	}

	return &ServerEventResult{
		Server: cloneServerRuntime(prepared.server), Transitions: transitions,
		Inventory: inventory, Outbox: &outboxCopy, Applied: true,
	}, nil
}

func serverGuardEpochKey(event ServerEvent) string {
	return strings.Join([]string{
		strings.TrimSpace(event.TenantID), strings.TrimSpace(event.ServerID),
		fmt.Sprintf("%d", event.Generation), strings.TrimSpace(event.SourceID),
		strings.TrimSpace(event.SourceEpoch),
	}, "\x00")
}
