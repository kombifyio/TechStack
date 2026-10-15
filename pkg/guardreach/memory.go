package guardreach

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryStore is the in-process Store of tests and single-node development.
// It mirrors the Postgres semantics: tenant scoping, leases and a forward-only
// cursor.
type MemoryStore struct {
	mu       sync.Mutex
	rows     map[Key]Row
	observed map[Key]bool
	cursors  map[string]int64
	now      func() time.Time
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		rows: map[Key]Row{}, observed: map[Key]bool{}, cursors: map[string]int64{},
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (s *MemoryStore) Enroll(_ context.Context, e Enrollment) error {
	tenantID, workerID, err := scope(e.TenantID, e.WorkerID)
	serverID, owner := strings.TrimSpace(e.ServerID), strings.TrimSpace(e.OwnerSubjectID)
	if err != nil || serverID == "" || owner == "" {
		return errors.New("guardreach: tenant, worker, server and owner are required")
	}
	connectTenant, observed := strings.TrimSpace(e.ConnectTenantID), true
	if connectTenant == "" || len(connectTenant) > 255 {
		observed = false
		var mapErr error
		if connectTenant, mapErr = ConnectTenant(tenantID, owner); mapErr != nil {
			connectTenant = ""
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := Key{tenantID, workerID}
	row, exists := s.rows[key]
	if !exists {
		now := s.now()
		s.rows[key] = Row{
			TenantID: tenantID, WorkerID: workerID, ServerID: serverID, StackID: strings.TrimSpace(e.StackID),
			OwnerSubjectID: owner, DisplayName: strings.TrimSpace(e.DisplayName), Placement: normalizePlacement(e.Placement),
			ConnectTenantID: connectTenant, OwnerRoles: StaffRoles(e.OwnerRoles), Desired: DesiredActive,
			Me: Target{State: TargetPending}, Connect: Target{State: TargetPending},
			NextAttemptAt: now, CreatedAt: now, UpdatedAt: now,
		}
		s.observed[key] = observed
		return nil
	}
	if row.ServerID != serverID || row.OwnerSubjectID != owner {
		return nil
	}
	if stack := strings.TrimSpace(e.StackID); stack != "" {
		row.StackID = stack
	}
	if name := strings.TrimSpace(e.DisplayName); name != "" {
		row.DisplayName = name
	}
	row.Placement = normalizePlacement(e.Placement)
	if roles := StaffRoles(e.OwnerRoles); len(roles) > 0 {
		row.OwnerRoles = roles
	}
	if observed && !s.observed[key] {
		row.ConnectTenantID = connectTenant
	}
	s.observed[key] = s.observed[key] || observed
	s.rows[key] = row
	return nil
}

func (s *MemoryStore) Get(_ context.Context, tenantID, workerID string) (*Row, error) {
	tenantID, workerID, err := scope(tenantID, workerID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[Key{tenantID, workerID}]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneRow(row), nil
}

func (s *MemoryStore) ListByTenant(_ context.Context, tenantID string) ([]Row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Row
	for key, row := range s.rows {
		if key.TenantID == strings.TrimSpace(tenantID) {
			out = append(out, *cloneRow(row))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkerID < out[j].WorkerID })
	return out, nil
}

func (s *MemoryStore) Update(_ context.Context, tenantID, workerID string, fn func(*Row) bool) (*Row, error) {
	tenantID, workerID, err := scope(tenantID, workerID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := Key{tenantID, workerID}
	stored, ok := s.rows[key]
	if !ok {
		return nil, ErrNotFound
	}
	row := cloneRow(stored)
	if fn(row) {
		row.UpdatedAt = s.now()
		s.rows[key] = *row
	}
	return cloneRow(*row), nil
}

func (s *MemoryStore) ListDue(_ context.Context, now time.Time, afterTenant, afterWorker string, limit int) ([]Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []Key
	for key, row := range s.rows {
		if !row.NextAttemptAt.After(now) && (key.TenantID > afterTenant || (key.TenantID == afterTenant && key.WorkerID > afterWorker)) {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].TenantID < keys[j].TenantID || (keys[i].TenantID == keys[j].TenantID && keys[i].WorkerID < keys[j].WorkerID)
	})
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
	}
	return keys, nil
}

func (s *MemoryStore) Claim(_ context.Context, tenantID, workerID string, now time.Time, lease time.Duration) (*Row, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := Key{strings.TrimSpace(tenantID), strings.TrimSpace(workerID)}
	row, ok := s.rows[key]
	if !ok || row.NextAttemptAt.After(now) {
		return nil, false, nil
	}
	row.NextAttemptAt = now.Add(lease)
	s.rows[key] = row
	return cloneRow(row), true, nil
}

func (s *MemoryStore) FindByConnect(_ context.Context, connectTenantID, serverID string) ([]Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []Key
	for key, row := range s.rows {
		if row.ConnectTenantID == strings.TrimSpace(connectTenantID) && row.ServerID == strings.TrimSpace(serverID) {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func (s *MemoryStore) Cursor(_ context.Context, name string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursors[name], nil
}

func (s *MemoryStore) AdvanceCursor(_ context.Context, name string, position int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if position > s.cursors[name] {
		s.cursors[name] = position
	}
	return nil
}

func cloneRow(row Row) *Row {
	row.OwnerRoles = append([]string(nil), row.OwnerRoles...)
	return &row
}

var _ Store = (*MemoryStore)(nil)
