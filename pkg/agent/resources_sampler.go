package agent

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// ResourceSampler periodically samples host-level resource usage (real CPU%,
// system memory, root-disk usage) and exposes the latest snapshot for
// heartbeats. It exists because a heartbeat must not block on CPU sampling:
// gopsutil derives CPU% from the delta between two reads, so the sampler keeps
// its own cadence and heartbeats read the most recent value lock-free.
type ResourceSampler struct {
	interval time.Duration
	log      *slog.Logger
	snapshot atomic.Pointer[agentpb.ResourceUsage]
}

// NewResourceSampler creates a sampler with the given interval (default 15s).
func NewResourceSampler(interval time.Duration, log *slog.Logger) *ResourceSampler {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if log == nil {
		log = nopLogger
	}
	return &ResourceSampler{
		interval: interval,
		log:      log.With("component", "resource-sampler"),
	}
}

// Snapshot returns the most recent resource usage, or nil before the first
// sample completes.
func (s *ResourceSampler) Snapshot() *agentpb.ResourceUsage {
	return s.snapshot.Load()
}

// Run samples until the context is canceled. The first CPU read primes
// gopsutil's delta baseline; every subsequent read reports usage since the
// previous one.
func (s *ResourceSampler) Run(ctx context.Context) {
	// Prime the CPU delta baseline (interval 0 = since last call).
	_, _ = cpu.PercentWithContext(ctx, 0, false)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sample(ctx)
		}
	}
}

func (s *ResourceSampler) sample(ctx context.Context) {
	usage := &agentpb.ResourceUsage{}

	if percents, err := cpu.PercentWithContext(ctx, 0, false); err == nil && len(percents) > 0 {
		usage.CpuPercent = percents[0]
	} else if err != nil {
		s.log.Debug("cpu_sample_failed", "error", err.Error())
	}

	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		usage.MemoryUsedBytes = clampUint64ToInt64(vm.Used)
		usage.MemoryTotalBytes = clampUint64ToInt64(vm.Total)
	} else {
		s.log.Debug("memory_sample_failed", "error", err.Error())
	}

	// Root-filesystem usage (platform-specific, implemented in resource_*.go).
	diskUsed, diskTotal := getDiskUsage()
	usage.DiskUsedBytes = clampUint64ToInt64(diskUsed)
	usage.DiskTotalBytes = clampUint64ToInt64(diskTotal)

	s.snapshot.Store(usage)
}
