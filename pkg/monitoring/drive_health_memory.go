package monitoring

import (
	"context"
	"encoding/json"
	"sort"
	"sync"

	"github.com/kombifyio/techstack/pkg/guardtelemetry"
	"github.com/google/uuid"
)

// MemoryDriveHealthStore is for isolated local probes; production uses PostgreSQL.
type memoryDriveEvent struct {
	id    string
	alert DriveAlert
}
type MemoryDriveHealthStore struct {
	mu      sync.Mutex
	states  map[string]DriveServerState
	pending []memoryDriveEvent
}

func NewMemoryDriveHealthStore() *MemoryDriveHealthStore {
	return &MemoryDriveHealthStore{states: map[string]DriveServerState{}}
}
func cloneDriveState(s DriveServerState) DriveServerState {
	body, _ := json.Marshal(s)
	var copy DriveServerState
	_ = json.Unmarshal(body, &copy)
	copy.TenantID = s.TenantID
	copy.SourceEpoch = s.SourceEpoch
	copy.Sequence = s.Sequence
	for i := range copy.Alerts {
		copy.Alerts[i].TenantID = s.TenantID
	}
	for i := range copy.Drives {
		for n := range copy.Drives[i].Alerts {
			copy.Drives[i].Alerts[n].TenantID = s.TenantID
		}
	}
	return copy
}
func (m *MemoryDriveHealthStore) Update(_ context.Context, id guardtelemetry.Identity, fn func(*DriveServerState) ([]DriveAlert, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := id.TenantID + "\x00" + id.ServerID
	s := cloneDriveState(m.states[key])
	s.TenantID = id.TenantID
	s.ServerID = id.ServerID
	if s.Drives == nil {
		s.Drives = []DriveState{}
	}
	if s.Alerts == nil {
		s.Alerts = []DriveAlert{}
	}
	events, err := fn(&s)
	if err != nil {
		return err
	}
	m.states[key] = cloneDriveState(s)
	for _, event := range events {
		m.pending = append(m.pending, memoryDriveEvent{id: uuid.NewString(), alert: event})
	}
	return nil
}
func (m *MemoryDriveHealthStore) List(_ context.Context, tenant, server string) ([]DriveServerState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []DriveServerState{}
	for _, s := range m.states {
		if s.TenantID == tenant && (server == "" || s.ServerID == server) {
			out = append(out, cloneDriveState(s))
		}
	}
	return out, nil
}
func (m *MemoryDriveHealthStore) Deliver(ctx context.Context, tenant string, n Notifier) error {
	m.mu.Lock()
	var pending []memoryDriveEvent
	var retained []memoryDriveEvent
	for _, event := range m.pending {
		if event.alert.TenantID == tenant {
			pending = append(pending, event)
		} else {
			retained = append(retained, event)
		}
	}
	m.pending = retained
	m.mu.Unlock()
	for i, a := range pending {
		notification := driveNotification(a.alert)
		notification.Labels["event_id"] = a.id
		if err := n.Notify(ctx, notification); err != nil {
			m.mu.Lock()
			m.pending = append(pending[i:], m.pending...)
			m.mu.Unlock()
			return err
		}
	}
	return nil
}

func (m *MemoryDriveHealthStore) ListTenants(_ context.Context, after string, limit int) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	for _, s := range m.states {
		if s.TenantID > after || (after == "" && s.TenantID == "") {
			seen[s.TenantID] = true
		}
	}
	out := []string{}
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
