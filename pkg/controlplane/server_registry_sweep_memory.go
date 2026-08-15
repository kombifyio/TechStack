package controlplane

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/serverregistry"
)

var _ serverregistry.SweepStore = (*MemoryStore)(nil)
var _ serverregistry.OutboxMaintenanceStore = (*MemoryStore)(nil)

// ListObservationSweepTenants derives the due tenant page directly from the
// aggregate heads; the in-memory store needs no wake-up directory.
func (s *MemoryStore) ListObservationSweepTenants(_ context.Context, afterTenantID string, limit int, heartbeatCutoff time.Time) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit < 1 {
		limit = 1
	}
	due := make(map[string]bool)
	for _, server := range s.servers {
		if server.TenantID <= strings.TrimSpace(afterTenantID) {
			continue
		}
		if !memorySweepTracked(server) {
			continue
		}
		if server.LastHeartbeatAt.UTC().Before(heartbeatCutoff.UTC()) {
			due[server.TenantID] = true
		}
	}
	tenants := make([]string, 0, len(due))
	for tenantID := range due {
		tenants = append(tenants, tenantID)
	}
	sort.Strings(tenants)
	if len(tenants) > limit {
		tenants = tenants[:limit]
	}
	return tenants, nil
}

// CompactObservationSweepTenant is a no-op: the memory listing is derived.
func (s *MemoryStore) CompactObservationSweepTenant(context.Context, string) error {
	return nil
}

func memorySweepTracked(server ServerRuntime) bool {
	if server.LastHeartbeatAt == nil || server.LastHeartbeatAt.IsZero() {
		return false
	}
	switch serverregistry.ConnectionState(server.ConnectionState) {
	case serverregistry.ConnectionConnected, serverregistry.ConnectionDegraded, serverregistry.ConnectionStale:
	default:
		return false
	}
	switch serverregistry.LifecycleState(server.LifecycleState) {
	case serverregistry.LifecycleDecommissioning, serverregistry.LifecycleDecommissioned:
		return false
	}
	return true
}

// ListOutboxPruneTenants derives the retention-expired tenant page from the
// in-memory outbox slice.
func (s *MemoryStore) ListOutboxPruneTenants(_ context.Context, afterTenantID string, limit int, retentionCutoff time.Time) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit < 1 {
		limit = 1
	}
	due := make(map[string]bool)
	for _, item := range s.serverOutbox {
		if item.TenantID <= strings.TrimSpace(afterTenantID) {
			continue
		}
		if item.CreatedAt.UTC().Before(retentionCutoff.UTC()) {
			due[item.TenantID] = true
		}
	}
	tenants := make([]string, 0, len(due))
	for tenantID := range due {
		tenants = append(tenants, tenantID)
	}
	sort.Strings(tenants)
	if len(tenants) > limit {
		tenants = tenants[:limit]
	}
	return tenants, nil
}

// PruneServerRegistryOutbox drops retention-expired rows for one tenant. The
// future K6 projector bootstraps via full aggregate resync, so pruning
// unclaimed history is safe (bead kombify-Techstack-nzy1.1).
func (s *MemoryStore) PruneServerRegistryOutbox(_ context.Context, tenantID string, retentionCutoff time.Time, batchLimit int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if batchLimit < 1 {
		batchLimit = 1
	}
	kept := s.serverOutbox[:0]
	var deleted int64
	for _, item := range s.serverOutbox {
		if deleted < int64(batchLimit) && item.TenantID == strings.TrimSpace(tenantID) && item.CreatedAt.UTC().Before(retentionCutoff.UTC()) {
			deleted++
			continue
		}
		kept = append(kept, item)
	}
	s.serverOutbox = kept
	return deleted, nil
}

// CompactOutboxPruneTenant is a no-op: the memory listing is derived.
func (s *MemoryStore) CompactOutboxPruneTenant(context.Context, string) error {
	return nil
}

// ServerRegistryOutboxStats reports the exact in-memory outbox size.
func (s *MemoryStore) ServerRegistryOutboxStats(context.Context) (serverregistry.OutboxStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stats := serverregistry.OutboxStats{EstimatedRows: int64(len(s.serverOutbox))}
	for _, item := range s.serverOutbox {
		created := item.CreatedAt.UTC()
		if stats.OldestCreatedAt == nil || created.Before(*stats.OldestCreatedAt) {
			stats.OldestCreatedAt = &created
		}
	}
	return stats, nil
}
