package vmleases

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

type MemoryStore struct {
	mu                   sync.RWMutex
	leases               map[vmlease.LeaseID]vmlease.Lease
	idempotencyKey       map[memoryIdempotencyKey]vmlease.LeaseID
	executionAuthorities map[memoryExecutionAuthorityKey]LeaseExecutionAuthority
	journal              []OperationEvent
	decommissioned       map[confirmedDecommissionKey]struct{}
}

type memoryIdempotencyKey struct {
	tenantID string
	key      string
}

type memoryExecutionAuthorityKey struct {
	tenantID string
	leaseID  vmlease.LeaseID
}

type confirmedDecommissionKey struct {
	tenantID                 string
	leaseID                  vmlease.LeaseID
	resourceGenerationDigest string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		leases:               make(map[vmlease.LeaseID]vmlease.Lease),
		idempotencyKey:       make(map[memoryIdempotencyKey]vmlease.LeaseID),
		executionAuthorities: make(map[memoryExecutionAuthorityKey]LeaseExecutionAuthority),
		journal:              []OperationEvent{},
		decommissioned:       make(map[confirmedDecommissionKey]struct{}),
	}
}

func (s *MemoryStore) ExecutionAuthority(_ context.Context, tenantID string, leaseID vmlease.LeaseID) (LeaseExecutionAuthority, error) {
	tenantID = strings.TrimSpace(tenantID)
	leaseID = vmlease.LeaseID(strings.TrimSpace(string(leaseID)))
	if tenantID == "" {
		return "", ErrTenantRequired
	}
	if leaseID == "" {
		return "", ErrNotFound
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	authority, ok := s.executionAuthorities[memoryExecutionAuthorityKey{tenantID: tenantID, leaseID: leaseID}]
	if !ok {
		return "", ErrLeaseExecutionAuthorityUnbound
	}
	if err := validateLeaseExecutionAuthority(authority); err != nil {
		return "", err
	}
	return normalizeLeaseExecutionAuthority(authority), nil
}

func (s *MemoryStore) Upsert(_ context.Context, lease vmlease.Lease, idempotencyKey string) (*vmlease.Lease, error) {
	tenantID, err := tenantIDFromLease(lease)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	created, err := s.upsertLocked(lease, tenantID, idempotencyKey)
	return created, err
}

func (s *MemoryStore) upsertLocked(lease vmlease.Lease, tenantID, idempotencyKey string) (*vmlease.Lease, error) {
	existingByID, leaseExists := s.leases[lease.ID]
	if leaseExists {
		existingTenantID, err := tenantIDFromLease(existingByID)
		if err != nil || existingTenantID != tenantID {
			return nil, ErrLeaseIdentityConflict
		}
	}
	normalizedIdempotencyKey := strings.TrimSpace(idempotencyKey)
	existingForKey, err := s.existingIdempotentLeaseLocked(tenantID, normalizedIdempotencyKey, lease.ID)
	if err != nil {
		return nil, err
	}
	if existingForKey != nil {
		return existingForKey, nil
	}
	if leaseExists {
		out := cloneLease(existingByID)
		return &out, nil
	}
	s.bindIdempotencyKeyLocked(tenantID, normalizedIdempotencyKey, lease.ID)
	s.leases[lease.ID] = cloneLease(lease)
	out := cloneLease(lease)
	return &out, nil
}

func (s *MemoryStore) existingIdempotentLeaseLocked(tenantID, idempotencyKey string, leaseID vmlease.LeaseID) (*vmlease.Lease, error) {
	if idempotencyKey == "" {
		return nil, nil
	}
	existingID, ok := s.idempotencyKey[memoryIdempotencyKey{tenantID: tenantID, key: idempotencyKey}]
	if !ok {
		return nil, nil
	}
	existing := s.leases[existingID]
	if existingID != leaseID {
		return nil, ErrLeaseIdentityConflict
	}
	out := cloneLease(existing)
	return &out, nil
}

func (s *MemoryStore) bindIdempotencyKeyLocked(tenantID, idempotencyKey string, leaseID vmlease.LeaseID) {
	if idempotencyKey == "" {
		return
	}
	// Preserve the first key already bound to an existing failed lease, just
	// like Postgres' COALESCE(existing.idempotency_key, excluded...). A new key
	// may identify a brand-new lease but must not re-key a replacement.
	for _, existingID := range s.idempotencyKey {
		if existingID == leaseID {
			return
		}
	}
	s.idempotencyKey[memoryIdempotencyKey{tenantID: tenantID, key: idempotencyKey}] = leaseID
}

func (s *MemoryStore) Get(_ context.Context, tenantID string, id vmlease.LeaseID) (*vmlease.Lease, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	lease, ok := s.leases[id]
	if !ok {
		return nil, ErrNotFound
	}
	if leaseTenantID, err := tenantIDFromLease(lease); err != nil || leaseTenantID != tenantID {
		return nil, ErrNotFound
	}
	out := cloneLease(lease)
	return &out, nil
}

func (s *MemoryStore) getInventory(_ context.Context, tenantID string, id vmlease.LeaseID) (*leaseInventoryRow, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	lease, ok := s.leases[id]
	if !ok {
		return nil, ErrNotFound
	}
	if leaseTenantID, err := tenantIDFromLease(lease); err != nil || leaseTenantID != tenantID {
		return nil, ErrNotFound
	}
	row := &leaseInventoryRow{lease: cloneLease(lease)}
	authority, bound := s.executionAuthorities[memoryExecutionAuthorityKey{tenantID: tenantID, leaseID: id}]
	if bound {
		row.authority = authority
		row.authorityBound = true
	}
	return row, nil
}

func (s *MemoryStore) ListByTenant(_ context.Context, tenantID string) ([]vmlease.Lease, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]vmlease.Lease, 0)
	for _, lease := range s.leases {
		leaseTenantID, err := tenantIDFromLease(lease)
		if err != nil || leaseTenantID != tenantID {
			continue
		}
		out = append(out, cloneLease(lease))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return string(out[i].ID) < string(out[j].ID)
	})
	return out, nil
}

func (s *MemoryStore) listInventoryByTenant(_ context.Context, tenantID string) ([]leaseInventoryRow, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]leaseInventoryRow, 0)
	for _, lease := range s.leases {
		leaseTenantID, err := tenantIDFromLease(lease)
		if err != nil || leaseTenantID != tenantID {
			continue
		}
		row := leaseInventoryRow{lease: cloneLease(lease)}
		authority, bound := s.executionAuthorities[memoryExecutionAuthorityKey{tenantID: tenantID, leaseID: lease.ID}]
		if bound {
			row.authority = authority
			row.authorityBound = true
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return string(out[i].lease.ID) < string(out[j].lease.ID)
	})
	return out, nil
}

func (s *MemoryStore) Update(_ context.Context, tenantID string, lease vmlease.Lease) (*vmlease.Lease, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	leaseTenantID, err := tenantIDFromLease(lease)
	if err != nil {
		return nil, err
	}
	if leaseTenantID != tenantID {
		return nil, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.leases[lease.ID]
	if !ok {
		return nil, ErrNotFound
	}
	existingTenantID, err := tenantIDFromLease(existing)
	if err != nil || existingTenantID != tenantID {
		return nil, ErrNotFound
	}
	if err := ensureResourceGenerationUnchanged(existing, lease); err != nil {
		return nil, err
	}
	if err := ensureCancellationMonotonic(existing, lease); err != nil {
		return nil, err
	}
	if err := ensureDecommissionClaimUnchanged(existing, lease); err != nil {
		return nil, err
	}
	s.leases[lease.ID] = cloneLease(lease)
	out := cloneLease(lease)
	return &out, nil
}

func (s *MemoryStore) AppendOperation(_ context.Context, event OperationEvent) error {
	if strings.TrimSpace(event.TenantID) == "" {
		return ErrTenantRequired
	}
	if event.LeaseID == "" {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.journal = append(s.journal, event)
	if event.EventType == OperationEventDecommission && event.Status == OperationStatusDecommissioned {
		if s.decommissioned == nil {
			s.decommissioned = make(map[confirmedDecommissionKey]struct{})
		}
		s.decommissioned[confirmedDecommissionKey{
			tenantID:                 strings.TrimSpace(event.TenantID),
			leaseID:                  event.LeaseID,
			resourceGenerationDigest: strings.TrimSpace(event.ResourceGenerationDigest),
		}] = struct{}{}
	}
	return nil
}

func (s *MemoryStore) HasConfirmedDecommission(_ context.Context, tenantID string, leaseID vmlease.LeaseID, resourceGenerationDigest string) (bool, error) {
	tenantID = strings.TrimSpace(tenantID)
	resourceGenerationDigest = strings.TrimSpace(resourceGenerationDigest)
	if tenantID == "" {
		return false, ErrTenantRequired
	}
	if leaseID == "" {
		return false, ErrNotFound
	}
	if !validResourceGenerationDigest(resourceGenerationDigest) {
		return false, ErrResourceGenerationDigest
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.decommissioned[confirmedDecommissionKey{
		tenantID:                 tenantID,
		leaseID:                  leaseID,
		resourceGenerationDigest: resourceGenerationDigest,
	}]
	return ok, nil
}

func (s *MemoryStore) ListOperations(_ context.Context, tenantID string, leaseID vmlease.LeaseID, limit int) ([]OperationEvent, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]OperationEvent, 0)
	for i := len(s.journal) - 1; i >= 0; i-- {
		event := s.journal[i]
		if event.TenantID != tenantID || event.LeaseID != leaseID {
			continue
		}
		out = append(out, event)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *MemoryStore) OperationJournal() []OperationEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]OperationEvent, len(s.journal))
	copy(out, s.journal)
	return out
}
