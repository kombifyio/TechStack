package controlplane

import (
	"context"
	"sort"
	"strings"
	"time"
)

var (
	_ InventoryMonitoringReader = (*MemoryStore)(nil)
	_ ServerMetricRecorder      = (*MemoryStore)(nil)
)

type memoryMetricKey struct {
	tenantID, serverID string
	resolution         time.Duration
}

// RecordServerMetricSample mirrors the Postgres bucket write and retention.
func (s *MemoryStore) RecordServerMetricSample(_ context.Context, tenantID, serverID string, at time.Time, sample MetricSample) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordServerMetricSampleLocked(strings.TrimSpace(tenantID), strings.TrimSpace(serverID), at, sample)
	return nil
}

func (s *MemoryStore) recordServerMetricSampleLocked(tenantID, serverID string, at time.Time, sample MetricSample) {
	if tenantID == "" || serverID == "" || (sample.CPU == nil && sample.Memory == nil && sample.Disk == nil) {
		return
	}
	if s.metricBuckets == nil {
		s.metricBuckets = make(map[memoryMetricKey]map[time.Time]*MetricBucket)
	}
	fineOpened := false
	for _, resolution := range []time.Duration{MetricFineResolution, MetricCoarseResolution} {
		key := memoryMetricKey{tenantID: tenantID, serverID: serverID, resolution: resolution}
		buckets := s.metricBuckets[key]
		if buckets == nil {
			buckets = make(map[time.Time]*MetricBucket)
			s.metricBuckets[key] = buckets
		}
		start := MetricBucketStart(at, resolution)
		bucket, ok := buckets[start]
		if !ok {
			bucket = &MetricBucket{Start: start}
			buckets[start] = bucket
			fineOpened = fineOpened || resolution == MetricFineResolution
		}
		bucket.Add(sample)
	}
	if !fineOpened {
		return
	}
	fineCutoff, coarseCutoff := MetricRetentionCutoffs(at)
	for resolution, cutoff := range map[time.Duration]time.Time{MetricFineResolution: fineCutoff, MetricCoarseResolution: coarseCutoff} {
		buckets := s.metricBuckets[memoryMetricKey{tenantID: tenantID, serverID: serverID, resolution: resolution}]
		for start := range buckets {
			if start.Before(cutoff) {
				delete(buckets, start)
			}
		}
	}
}

func (s *MemoryStore) visibleServersOrderedLocked(scope InventoryReadScope) []ServerRuntime {
	servers := make([]ServerRuntime, 0)
	for _, server := range s.servers {
		if s.inventoryServerVisibleLocked(server, scope) {
			servers = append(servers, server)
		}
	}
	sort.Slice(servers, func(i, j int) bool {
		return compareInventoryPageKeys(inventoryPageKey(servers[i].CreatedAt, servers[i].ID), inventoryPageKey(servers[j].CreatedAt, servers[j].ID)) < 0
	})
	if len(servers) > InventoryMonitoringServerLimit {
		servers = servers[:InventoryMonitoringServerLimit]
	}
	return servers
}

// ListInventoryMetricHistory implements InventoryMonitoringReader.
func (s *MemoryStore) ListInventoryMetricHistory(_ context.Context, scope InventoryReadScope, window MetricRange, now time.Time) ([]ServerMetricSeries, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !inventoryScopeAllowsServerCollection(scope) {
		return nil, ErrInvalidInventoryReadScope
	}
	from := MetricBucketStart(now.UTC().Add(-window.Window), window.Step)
	series := make([]ServerMetricSeries, 0)
	for _, server := range s.visibleServersOrderedLocked(scope) {
		stored := s.metricBuckets[memoryMetricKey{tenantID: scope.tenantID, serverID: server.ID, resolution: window.Resolution}]
		buckets := make([]MetricBucket, 0, len(stored))
		for start, bucket := range stored {
			if !start.Before(from) && !start.After(now.UTC()) {
				buckets = append(buckets, *bucket)
			}
		}
		series = append(series, ServerMetricSeries{ServerID: server.ID, Points: DownsampleMetricBuckets(buckets, window.Step)})
	}
	return series, nil
}

// ListInventoryTransitions implements InventoryMonitoringReader. The in-memory
// store keeps no service timeline; it serves server transitions only.
func (s *MemoryStore) ListInventoryTransitions(_ context.Context, scope InventoryReadScope, since time.Time, limit int) ([]InventoryTransitionEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !inventoryScopeAllowsServerCollection(scope) {
		return nil, ErrInvalidInventoryReadScope
	}
	limit = clampTransitionLimit(limit)
	events := make([]InventoryTransitionEvent, 0)
	for _, server := range s.visibleServersOrderedLocked(scope) {
		label := firstNonEmpty(server.DisplayName, server.Name)
		for _, transition := range s.serverTransitions[server.ID] {
			field := monitoringTransitionField("server", transition.Dimension)
			if field == "" || transition.FromState == "" || transition.ObservedAt.Before(since.UTC()) {
				continue
			}
			events = append(events, InventoryTransitionEvent{
				At: transition.ObservedAt.UTC(), Kind: "server", ID: server.ID, Name: label, ServerID: server.ID,
				Field: field, From: transition.FromState, To: transition.ToState, ReasonCode: transition.ReasonCode,
			})
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].At.After(events[j].At) })
	if len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}

// ListInventoryBackupFacts implements InventoryMonitoringReader. The in-memory
// store holds no backup custody, schedule or job history, so it reports only
// the identity of each visible server.
func (s *MemoryStore) ListInventoryBackupFacts(_ context.Context, scope InventoryReadScope, _ time.Time) ([]InventoryBackupFacts, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !inventoryScopeAllowsServerCollection(scope) {
		return nil, ErrInvalidInventoryReadScope
	}
	facts := make([]InventoryBackupFacts, 0)
	for _, server := range s.visibleServersOrderedLocked(scope) {
		facts = append(facts, InventoryBackupFacts{ServerID: server.ID, StackID: server.StackID, WorkerID: server.WorkerID})
	}
	return facts, nil
}
