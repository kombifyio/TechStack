package serverregistry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/runtimehealth"
)

const (
	// DefaultSweepInterval is how often the observation sweeper demotes stale
	// Guard evidence and prunes outbox history.
	DefaultSweepInterval = 30 * time.Second
	// DefaultOutboxRetention bounds server_registry_outbox history. The future
	// K6 central projector bootstraps via full aggregate resync, not by
	// replaying outbox history, so pruning unclaimed rows older than the
	// retention window is safe (commitment recorded on bead
	// kombify-Techstack-nzy1.1).
	DefaultOutboxRetention = 30 * 24 * time.Hour

	defaultSweepTenantPageSize   = 50
	defaultSweepMaxTenantsPerRun = 500
	defaultOutboxPruneBatch      = 1000
	maxOutboxPruneBatchesPerRun  = 100

	// SweeperSource labels sweeper-written transitions and outbox events.
	SweeperSource = "registry-sweeper"

	// ReasonHeartbeatStale marks a connection/health demotion because the last
	// authenticated Guard heartbeat aged past the fresh window.
	ReasonHeartbeatStale = "heartbeat_stale"
	// ReasonHeartbeatExpired marks a demotion to offline because the last
	// authenticated Guard heartbeat aged past the stale window.
	ReasonHeartbeatExpired = "heartbeat_expired"
)

// SweepStore is the tenant-scoped registry surface the sweeper drives. Every
// demotion is a WRITE through the single ApplyServerEvent boundary; the
// sweeper never mutates rows directly.
type SweepStore interface {
	Repository
	// ListObservationSweepTenants pages tenant IDs whose earliest tracked
	// Guard heartbeat is older than the cutoff.
	ListObservationSweepTenants(ctx context.Context, afterTenantID string, limit int, heartbeatCutoff time.Time) ([]string, error)
	// ListServerRuntimesByTenant returns the tenant's aggregate heads.
	ListServerRuntimesByTenant(ctx context.Context, tenantID, stackID string) ([]Aggregate, error)
	// CompactObservationSweepTenant refreshes or retires the tenant's wake-up
	// entry after a sweep pass. Implementations without a directory no-op.
	CompactObservationSweepTenant(ctx context.Context, tenantID string) error
}

// OutboxStats is the secret-free size signal for server_registry_outbox.
type OutboxStats struct {
	// EstimatedRows is a planner estimate (pg_class.reltuples); it can lag
	// behind exact counts but is cheap enough for a 30s gauge.
	EstimatedRows int64
	// OldestCreatedAt is the oldest unpruned outbox row across tenants.
	OldestCreatedAt *time.Time
}

// OutboxMaintenanceStore bounds server_registry_outbox until the K6 projector
// lands: retention pruning plus a size gauge for the monitoring path.
type OutboxMaintenanceStore interface {
	ListOutboxPruneTenants(ctx context.Context, afterTenantID string, limit int, retentionCutoff time.Time) ([]string, error)
	// PruneServerRegistryOutbox deletes at most batchLimit rows older than the
	// cutoff inside the tenant's RLS scope and reports the deleted count.
	PruneServerRegistryOutbox(ctx context.Context, tenantID string, retentionCutoff time.Time, batchLimit int) (int64, error)
	// CompactOutboxPruneTenant refreshes or retires the tenant's prune wake-up
	// entry after a prune pass.
	CompactOutboxPruneTenant(ctx context.Context, tenantID string) error
	ServerRegistryOutboxStats(ctx context.Context) (OutboxStats, error)
}

// SweeperConfig wires the observation sweeper.
type SweeperConfig struct {
	Registry SweepStore
	// Outbox enables retention pruning and the size gauge when non-nil.
	Outbox           OutboxMaintenanceStore
	Interval         time.Duration
	OutboxRetention  time.Duration
	TenantPageSize   int
	MaxTenantsPerRun int
	OutboxPruneBatch int
	Now              func() time.Time
	Logger           *slog.Logger
	OnError          func(error)
	// IsBenignConflict filters expected CAS/admission losses (another instance
	// or the Guard itself won the revision race) out of the error path.
	IsBenignConflict  func(error) bool
	RecordOutboxStats func(OutboxStats)
}

// Sweeper persists honest observed state: it demotes server connection/health
// through ApplyServerEvent (Guard authority, fenced by the aggregate's
// CAS/epoch admission) whenever the persisted heartbeat ages past the
// pkg/runtimehealth freshness windows, and it bounds server_registry_outbox.
//
// Multi-instance safety needs no extra locking: every demotion carries the
// observed aggregate revision (compare-and-swap) plus the current Guard source
// epoch at the next sequence, so concurrent sweepers and late Guard events are
// arbitrated by the existing admission rules; losers surface as benign
// conflicts or unapplied replays.
type Sweeper struct {
	registry          SweepStore
	outbox            OutboxMaintenanceStore
	interval          time.Duration
	outboxRetention   time.Duration
	tenantPageSize    int
	maxTenantsPerRun  int
	outboxPruneBatch  int
	now               func() time.Time
	logger            *slog.Logger
	onError           func(error)
	isBenignConflict  func(error) bool
	recordOutboxStats func(OutboxStats)
}

// SweepResult reports one demotion/prune pass for logging and tests.
type SweepResult struct {
	TenantsSwept    int
	Demotions       int
	BenignConflicts int
	OutboxPruned    int64
}

func NewSweeper(cfg SweeperConfig) (*Sweeper, error) {
	if cfg.Registry == nil {
		return nil, fmt.Errorf("serverregistry: sweeper registry store is required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultSweepInterval
	}
	if cfg.OutboxRetention <= 0 {
		cfg.OutboxRetention = DefaultOutboxRetention
	}
	if cfg.TenantPageSize <= 0 || cfg.TenantPageSize > 100 {
		cfg.TenantPageSize = defaultSweepTenantPageSize
	}
	if cfg.MaxTenantsPerRun <= 0 {
		cfg.MaxTenantsPerRun = defaultSweepMaxTenantsPerRun
	}
	if cfg.OutboxPruneBatch <= 0 {
		cfg.OutboxPruneBatch = defaultOutboxPruneBatch
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.IsBenignConflict == nil {
		cfg.IsBenignConflict = func(error) bool { return false }
	}
	return &Sweeper{
		registry:          cfg.Registry,
		outbox:            cfg.Outbox,
		interval:          cfg.Interval,
		outboxRetention:   cfg.OutboxRetention,
		tenantPageSize:    cfg.TenantPageSize,
		maxTenantsPerRun:  cfg.MaxTenantsPerRun,
		outboxPruneBatch:  cfg.OutboxPruneBatch,
		now:               cfg.Now,
		logger:            cfg.Logger,
		onError:           cfg.OnError,
		isBenignConflict:  cfg.IsBenignConflict,
		recordOutboxStats: cfg.RecordOutboxStats,
	}, nil
}

// Run sweeps immediately and then on every interval tick until ctx ends.
func (s *Sweeper) Run(ctx context.Context) {
	if s == nil {
		return
	}
	s.runPass(ctx)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runPass(ctx)
		}
	}
}

func (s *Sweeper) runPass(ctx context.Context) {
	result, err := s.SweepOnce(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		s.reportError(err)
	}
	if result.Demotions > 0 || result.OutboxPruned > 0 {
		s.logger.Info("server_registry_sweep",
			"tenants", result.TenantsSwept,
			"demotions", result.Demotions,
			"benign_conflicts", result.BenignConflicts,
			"outbox_pruned", result.OutboxPruned,
		)
	}
}

// SweepOnce runs one full demotion plus outbox-maintenance pass.
func (s *Sweeper) SweepOnce(ctx context.Context) (SweepResult, error) {
	var result SweepResult
	if s == nil || s.registry == nil {
		return result, fmt.Errorf("serverregistry: sweeper is not configured")
	}
	demotionErr := s.sweepObservations(ctx, &result)
	outboxErr := s.maintainOutbox(ctx, &result)
	return result, errors.Join(demotionErr, outboxErr)
}

func (s *Sweeper) sweepObservations(ctx context.Context, result *SweepResult) error {
	now := s.now().UTC()
	cutoff := now.Add(-runtimehealth.FreshHeartbeatWindow)
	afterTenant := ""
	for result.TenantsSwept < s.maxTenantsPerRun {
		limit := s.tenantPageSize
		if remaining := s.maxTenantsPerRun - result.TenantsSwept; limit > remaining {
			limit = remaining
		}
		tenants, err := s.registry.ListObservationSweepTenants(ctx, afterTenant, limit, cutoff)
		if err != nil {
			return fmt.Errorf("serverregistry: list sweep tenants: %w", err)
		}
		if len(tenants) == 0 {
			return nil
		}
		for _, tenantID := range tenants {
			afterTenant = tenantID
			result.TenantsSwept++
			if err := s.sweepTenant(ctx, tenantID, result); err != nil {
				if errors.Is(err, context.Canceled) {
					return err
				}
				s.reportError(fmt.Errorf("serverregistry: sweep tenant %s: %w", tenantID, err))
			}
			if err := s.registry.CompactObservationSweepTenant(ctx, tenantID); err != nil && !errors.Is(err, context.Canceled) {
				s.reportError(fmt.Errorf("serverregistry: compact sweep tenant %s: %w", tenantID, err))
			}
		}
		if len(tenants) < limit {
			return nil
		}
	}
	return nil
}

func (s *Sweeper) sweepTenant(ctx context.Context, tenantID string, result *SweepResult) error {
	servers, err := s.registry.ListServerRuntimesByTenant(ctx, tenantID, "")
	if err != nil {
		return err
	}
	now := s.now().UTC()
	for i := range servers {
		command, due := DemotionCommand(now, servers[i])
		if !due {
			continue
		}
		applied, applyErr := s.registry.ApplyServerEvent(ctx, command)
		switch {
		case applyErr == nil && applied != nil && applied.Applied:
			result.Demotions++
		case applyErr == nil:
			// Unapplied replay: the Guard or another sweeper superseded this
			// observation between the read and the write. Nothing to record.
			result.BenignConflicts++
		case s.isBenignConflict(applyErr):
			result.BenignConflicts++
		case errors.Is(applyErr, context.Canceled):
			return applyErr
		default:
			s.reportError(fmt.Errorf("serverregistry: demote server %s/%s: %w", tenantID, servers[i].ID, applyErr))
		}
	}
	return nil
}

func (s *Sweeper) maintainOutbox(ctx context.Context, result *SweepResult) error {
	if s.outbox == nil {
		return nil
	}
	if stats, err := s.outbox.ServerRegistryOutboxStats(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		s.reportError(fmt.Errorf("serverregistry: outbox stats: %w", err))
	} else if s.recordOutboxStats != nil {
		s.recordOutboxStats(stats)
	}

	cutoff := s.now().UTC().Add(-s.outboxRetention)
	afterTenant := ""
	tenantsPruned := 0
	for tenantsPruned < s.maxTenantsPerRun {
		limit := s.tenantPageSize
		if remaining := s.maxTenantsPerRun - tenantsPruned; limit > remaining {
			limit = remaining
		}
		tenants, err := s.outbox.ListOutboxPruneTenants(ctx, afterTenant, limit, cutoff)
		if err != nil {
			return fmt.Errorf("serverregistry: list outbox prune tenants: %w", err)
		}
		if len(tenants) == 0 {
			return nil
		}
		for _, tenantID := range tenants {
			afterTenant = tenantID
			tenantsPruned++
			pruned, pruneErr := s.pruneTenantOutbox(ctx, tenantID, cutoff)
			result.OutboxPruned += pruned
			if pruneErr != nil {
				if errors.Is(pruneErr, context.Canceled) {
					return pruneErr
				}
				s.reportError(fmt.Errorf("serverregistry: prune outbox tenant %s: %w", tenantID, pruneErr))
			}
			if pruned > 0 {
				s.logger.Info("server_registry_outbox_pruned", "tenant_id", tenantID, "rows", pruned, "retention", s.outboxRetention.String())
			}
			if err := s.outbox.CompactOutboxPruneTenant(ctx, tenantID); err != nil && !errors.Is(err, context.Canceled) {
				s.reportError(fmt.Errorf("serverregistry: compact outbox prune tenant %s: %w", tenantID, err))
			}
		}
		if len(tenants) < limit {
			return nil
		}
	}
	return nil
}

func (s *Sweeper) pruneTenantOutbox(ctx context.Context, tenantID string, cutoff time.Time) (int64, error) {
	var total int64
	for batch := 0; batch < maxOutboxPruneBatchesPerRun; batch++ {
		deleted, err := s.outbox.PruneServerRegistryOutbox(ctx, tenantID, cutoff, s.outboxPruneBatch)
		total += deleted
		if err != nil {
			return total, err
		}
		if deleted < int64(s.outboxPruneBatch) {
			return total, nil
		}
	}
	return total, nil
}

func (s *Sweeper) reportError(err error) {
	if s.onError != nil {
		s.onError(err)
		return
	}
	s.logger.Error("server_registry_sweep_error", "error", err)
}

// DemotionCommand derives the sweeper's write for one aggregate head. It
// returns false when the server carries no demotable Guard evidence, when the
// heartbeat is still fresh, or when the persisted state already matches the
// derived state (change-only: no no-op transitions).
func DemotionCommand(now time.Time, server Aggregate) (Command, bool) {
	if server.LastHeartbeatAt == nil || server.LastHeartbeatAt.IsZero() {
		return Command{}, false
	}
	// Only Guard-checkpointed rows can be demoted through Guard authority.
	// Rows without a source epoch (legacy projections, freshly fenced
	// generations) have no admission position to extend.
	if strings.TrimSpace(server.WorkerID) == "" ||
		strings.TrimSpace(server.SourceEpoch) == "" ||
		server.SourceAuthority != AuthorityGuard ||
		strings.TrimSpace(server.SourceID) != strings.TrimSpace(server.WorkerID) {
		return Command{}, false
	}
	switch LifecycleState(strings.TrimSpace(server.LifecycleState)) {
	case LifecycleDecommissioning, LifecycleDecommissioned:
		return Command{}, false
	}
	switch ConnectionState(strings.TrimSpace(server.ConnectionState)) {
	case ConnectionConnected, ConnectionDegraded, ConnectionStale:
	default:
		// pending/connecting carry no heartbeat promise; revoked and offline
		// are already terminal observations.
		return Command{}, false
	}

	observedHostState := ""
	if value, ok := server.Metadata["observed_host_state"].(string); ok {
		observedHostState = value
	}
	connection, health := DeriveObservedState(now, server.LastHeartbeatAt, observedHostState)
	if connection != ConnectionStale && connection != ConnectionOffline {
		return Command{}, false
	}
	if server.ConnectionState == string(connection) && server.HealthState == string(health) {
		return Command{}, false
	}
	reason := ReasonHeartbeatStale
	if connection == ConnectionOffline {
		reason = ReasonHeartbeatExpired
	}
	heartbeatAge := now.Sub(server.LastHeartbeatAt.UTC())
	return Command{
		TenantID:         server.TenantID,
		ServerID:         server.ID,
		ExpectedRevision: server.Revision,
		Generation:       server.Generation,
		Authority:        AuthorityGuard,
		Source:           SweeperSource,
		// The demotion extends the current Guard admission position: same
		// source and epoch, next sequence. A resumed Guard re-takes ownership
		// with its next accepted heartbeat (or a fresh epoch after restart).
		SourceID:       server.WorkerID,
		SourceEpoch:    server.SourceEpoch,
		SourceSequence: server.SourceSequence + 1,
		ObservedAt:     now,
		Runtime: Aggregate{
			ConnectionState:      string(connection),
			HealthState:          string(health),
			ConnectionReasonCode: reason,
			HealthReasonCode:     reason,
		},
		Evidence: map[string]any{
			"sweep":                 true,
			"last_heartbeat_at":     server.LastHeartbeatAt.UTC().Format(time.RFC3339Nano),
			"heartbeat_age_seconds": int64(heartbeatAge.Seconds()),
		},
	}, true
}
