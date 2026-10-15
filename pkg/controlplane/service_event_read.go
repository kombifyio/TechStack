package controlplane

import (
	"context"
	"database/sql"
	"errors"
)

// ServiceEventReader reads the existing aggregate fence for retained plans.
// The eventual assertion still passes through ApplyServiceEvent's row lock.
type ServiceEventReader interface {
	ReadServiceEventHead(context.Context, string, string) (*ServiceEventResult, error)
}

func (s *PostgresStore) ReadServiceEventHead(ctx context.Context, tenant, id string) (*ServiceEventResult, error) {
	var out *ServiceEventResult
	err := s.withTenant(ctx, tenant, func(tx *sql.Tx) error {
		head, err := scanServiceAggregateHead(tx.QueryRowContext(ctx, "SELECT "+serviceAggregateColumns+" FROM services WHERE tenant_id=$1 AND id=$2", tenant, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = &ServiceEventResult{Service: cloneServiceRuntime(head.Runtime), Revision: head.Revision, Status: head.Status}
		return nil
	})
	return out, err
}
func (s *MemoryStore) ReadServiceEventHead(_ context.Context, tenant, id string) (*ServiceEventResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.serviceRuntime[id]
	if !ok || v.TenantID != tenant {
		return nil, ErrNotFound
	}
	return &ServiceEventResult{Service: cloneServiceRuntime(v), Revision: v.Revision}, nil
}
func (s *MemoryStore) ApplyServiceEvent(_ context.Context, event ServiceEvent) (*ServiceEventResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var current *serviceAggregateHead
	if v, ok := s.serviceRuntime[event.ServiceID]; ok && v.TenantID == event.TenantID {
		current = &serviceAggregateHead{Runtime: v, Revision: v.Revision, Exists: true}
	}
	prepared, err := prepareServiceEvent(current, event, s.now())
	if err != nil {
		return nil, err
	}
	head := prepared.head
	head.Runtime.Revision = head.Revision
	s.serviceRuntime[event.ServiceID] = head.Runtime
	return &ServiceEventResult{Service: cloneServiceRuntime(head.Runtime), Revision: head.Revision, Status: head.Status, Transitions: prepared.transitions, Applied: prepared.applied}, nil
}
