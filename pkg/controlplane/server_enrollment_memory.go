package controlplane

import (
	"context"
)

func (s *MemoryStore) ApplyServerEnrollment(_ context.Context, command ServerEnrollment) (*ServerEnrollmentResult, error) {
	prepared, err := prepareServerEnrollment(command)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, exists := s.nodes[prepared.Node.ID]
	if exists {
		if err := validateExistingEnrollmentNode(existing, prepared.Node); err != nil {
			return nil, err
		}
	}
	result, err := s.applyServerEventLocked(prepared.Event)
	if err != nil {
		return nil, err
	}
	if !exists {
		node := prepared.Node
		now := s.now()
		node.CreatedAt, node.UpdatedAt = now, now
		node.Metadata = cloneMap(node.Metadata)
		s.nodes[node.ID] = node
	}
	var worker *Worker
	if prepared.Worker != nil {
		worker = s.upsertWorkerHeartbeatLocked(*prepared.Worker)
	}
	return &ServerEnrollmentResult{ServerEventResult: result, Worker: worker}, nil
}

var _ ServerEnrollmentStore = (*MemoryStore)(nil)
