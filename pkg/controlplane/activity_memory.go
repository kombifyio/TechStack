package controlplane

import (
	"context"
	"sort"
	"strings"
)

func (s *MemoryStore) AppendActivity(_ context.Context, event ActivityEvent) (*ActivityEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	event = normalizeActivityEvent(event)
	if event.CreatedAt.IsZero() {
		event.CreatedAt = s.now()
	}
	event.Details = cloneMap(event.Details)
	s.events[event.ID] = event
	return cloneActivityEvent(event), nil
}

func (s *MemoryStore) ListActivity(ctx context.Context, tenantID, stackID string, limit int) ([]ActivityEvent, error) {
	return s.ListActivityScoped(ctx, tenantID, ActivityFilter{StackID: stackID, Limit: limit})
}

func (s *MemoryStore) ListActivityScoped(_ context.Context, tenantID string, filter ActivityFilter) ([]ActivityEvent, error) {
	tenantID = strings.TrimSpace(tenantID)
	filter.StackID = strings.TrimSpace(filter.StackID)
	filter.RuntimeScopeKey = strings.TrimSpace(filter.RuntimeScopeKey)
	filter.ServerScopeKey = strings.TrimSpace(filter.ServerScopeKey)
	filter.ServiceScopeKey = strings.TrimSpace(filter.ServiceScopeKey)
	if filter.Limit <= 0 {
		filter.Limit = defaultActivityLimit
	}
	if filter.Limit > 200 {
		filter.Limit = 200
	}

	s.mu.RLock()
	out := make([]ActivityEvent, 0)
	for _, event := range s.events {
		if event.TenantID != tenantID {
			continue
		}
		if filter.StackID != "" && event.StackID != filter.StackID {
			continue
		}
		if filter.RuntimeScopeKey != "" && event.RuntimeScopeKey != filter.RuntimeScopeKey {
			continue
		}
		if filter.ServerScopeKey != "" && event.ServerScopeKey != filter.ServerScopeKey {
			continue
		}
		if filter.ServiceScopeKey != "" && event.ServiceScopeKey != filter.ServiceScopeKey {
			continue
		}
		if !filter.CursorCreatedAt.IsZero() && (event.CreatedAt.After(filter.CursorCreatedAt) || (event.CreatedAt.Equal(filter.CursorCreatedAt) && event.ID >= filter.CursorID)) {
			continue
		}
		out = append(out, *cloneActivityEvent(event))
	}
	s.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if len(out) > filter.Limit {
		return out[:filter.Limit], nil
	}
	return out, nil
}

var _ ActivityStore = (*MemoryStore)(nil)
