package serverregistry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// PlatformProjector publishes the registry's current server state into the
// kombify platform database, so every product can answer "which machines does
// this user have?" from one place instead of calling Techstack and inheriting
// its inventory authorization (DATA-ARCHITECTURE row 1 `infra_user_servers`;
// ADR-038; owner direction 2026-09-18).
//
// It republishes CURRENT aggregate state rather than replaying outbox history —
// the resync bootstrap PruneServerRegistryOutbox already assumes ("the future K6
// projector bootstraps via full aggregate resync, never by replaying
// server_registry_outbox history"). That is what makes outbox pruning safe and
// this worker restartable: every pass is a complete, idempotent statement of
// what the registry believes right now, and the receiving side refuses any row
// whose revision is older than the one it holds.
//
// The tenant index it walks is maintained beside the outbox, so a tenant enters
// the sweep the moment its first server event lands.

// ProjectedServer is one server as the platform list stores it. Field names are
// the wire contract with the platform ingest endpoint.
type ProjectedServer struct {
	ServerID        string `json:"server_id"`
	TenantID        string `json:"tenant_id"`
	OwnerID         string `json:"owner_id"`
	OrgID           string `json:"org_id,omitempty"`
	Name            string `json:"name,omitempty"`
	Provider        string `json:"provider,omitempty"`
	StackID         string `json:"stack_id,omitempty"`
	StackkitName    string `json:"stackkit_name,omitempty"`
	StackkitVersion string `json:"stackkit_version,omitempty"`
	LifecycleState  string `json:"lifecycle_state,omitempty"`
	ConnectionState string `json:"connection_state,omitempty"`
	HealthState     string `json:"health_state,omitempty"`
	// PlatformOS and ServiceNames exist because products display them: the
	// platform list has to carry what a surface shows, or every surface goes
	// back to asking this service directly.
	PlatformOS        string   `json:"platform_os,omitempty"`
	ServiceNames      []string `json:"service_names,omitempty"`
	ServiceCount      int      `json:"service_count"`
	AggregateRevision int64    `json:"aggregate_revision"`
	Retired           bool     `json:"retired,omitempty"`
	ObservedAt        string   `json:"observed_at,omitempty"`
}

// PlatformPublisher delivers one tenant's servers to the platform database.
// The transport is the caller's concern; the projector only cares that a batch
// either lands or reports an error worth retrying on the next pass.
type PlatformPublisher interface {
	PublishServers(ctx context.Context, tenantID string, servers []ProjectedServer) error
}

// ProjectionSource reads what the projector publishes. Both halves stay behind
// an interface so the worker is testable without a database.
type ProjectionSource interface {
	// ListServerProjectionTenants pages the tenants the registry knows.
	ListServerProjectionTenants(ctx context.Context, afterTenantID string, limit int) ([]string, error)
	// ListProjectedServers returns one tenant's current servers.
	ListProjectedServers(ctx context.Context, tenantID string) ([]ProjectedServer, error)
}

type PlatformProjectorConfig struct {
	Source    ProjectionSource
	Publisher PlatformPublisher
	// Interval between full passes. Defaults to two minutes.
	Interval time.Duration
	// TenantPageSize bounds one tenant page; MaxTenantsPerRun bounds a pass.
	TenantPageSize   int
	MaxTenantsPerRun int
	// BatchSize bounds one publish so a large tenant cannot exceed the
	// receiver's body limit.
	BatchSize int
	Now       func() time.Time
	Logger    *slog.Logger
	OnError   func(error)
}

type PlatformProjector struct {
	source           ProjectionSource
	publisher        PlatformPublisher
	interval         time.Duration
	tenantPageSize   int
	maxTenantsPerRun int
	batchSize        int
	now              func() time.Time
	logger           *slog.Logger
	onError          func(error)
}

// ProjectionResult reports one pass. Tenants that failed are counted, not
// dropped: the next pass republishes them, because state is resent in full.
type ProjectionResult struct {
	Tenants  int
	Servers  int
	Failures int
}

func NewPlatformProjector(cfg PlatformProjectorConfig) (*PlatformProjector, error) {
	if cfg.Source == nil {
		return nil, errors.New("serverregistry: projection source required")
	}
	if cfg.Publisher == nil {
		return nil, errors.New("serverregistry: platform publisher required")
	}
	p := &PlatformProjector{
		source:           cfg.Source,
		publisher:        cfg.Publisher,
		interval:         cfg.Interval,
		tenantPageSize:   cfg.TenantPageSize,
		maxTenantsPerRun: cfg.MaxTenantsPerRun,
		batchSize:        cfg.BatchSize,
		now:              cfg.Now,
		logger:           cfg.Logger,
		onError:          cfg.OnError,
	}
	if p.interval <= 0 {
		p.interval = 2 * time.Minute
	}
	if p.tenantPageSize < 1 || p.tenantPageSize > 100 {
		p.tenantPageSize = 50
	}
	if p.maxTenantsPerRun < 1 {
		p.maxTenantsPerRun = 500
	}
	if p.batchSize < 1 || p.batchSize > 200 {
		p.batchSize = 100
	}
	if p.now == nil {
		p.now = time.Now
	}
	if p.logger == nil {
		p.logger = slog.Default()
	}
	return p, nil
}

// Run projects immediately and then on the configured interval until the
// context ends. A failing pass never stops the loop: the platform list is a
// derived view, and the next pass restates it.
func (p *PlatformProjector) Run(ctx context.Context) {
	if p == nil {
		return
	}
	p.runPass(ctx)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.runPass(ctx)
		}
	}
}

func (p *PlatformProjector) runPass(ctx context.Context) {
	result, err := p.ProjectOnce(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		p.reportError(err)
		return
	}
	if result.Servers > 0 || result.Failures > 0 {
		p.logger.Info("serverregistry: platform projection pass",
			"tenants", result.Tenants, "servers", result.Servers, "failures", result.Failures)
	}
}

// ProjectOnce publishes every known tenant's current servers once.
func (p *PlatformProjector) ProjectOnce(ctx context.Context) (ProjectionResult, error) {
	var result ProjectionResult
	afterTenant := ""
	for result.Tenants < p.maxTenantsPerRun {
		limit := p.tenantPageSize
		if remaining := p.maxTenantsPerRun - result.Tenants; limit > remaining {
			limit = remaining
		}
		tenants, err := p.source.ListServerProjectionTenants(ctx, afterTenant, limit)
		if err != nil {
			return result, fmt.Errorf("serverregistry: list projection tenants: %w", err)
		}
		if len(tenants) == 0 {
			break
		}
		for _, tenantID := range tenants {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			// The budget is enforced here too, not only when asking for the
			// next page: a source that returns more rows than the limit must
			// not be able to stretch one pass past its bound.
			if result.Tenants >= p.maxTenantsPerRun {
				return result, nil
			}
			afterTenant = tenantID
			result.Tenants++
			published, err := p.projectTenant(ctx, tenantID)
			if err != nil {
				result.Failures++
				p.reportError(fmt.Errorf("serverregistry: project tenant %q: %w", tenantID, err))
				continue
			}
			result.Servers += published
		}
		if len(tenants) < limit {
			break
		}
	}
	return result, nil
}

func (p *PlatformProjector) projectTenant(ctx context.Context, tenantID string) (int, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return 0, nil
	}
	servers, err := p.source.ListProjectedServers(ctx, tenantID)
	if err != nil {
		return 0, fmt.Errorf("list servers: %w", err)
	}
	if len(servers) == 0 {
		// An empty tenant is still a statement worth publishing: without it the
		// platform cannot tell "this owner has no machines" from "the projector
		// has not reached this owner yet", and every new account is labelled
		// unprojected forever.
		if err := p.publisher.PublishServers(ctx, tenantID, nil); err != nil {
			return 0, fmt.Errorf("publish empty: %w", err)
		}
		return 0, nil
	}
	published := 0
	for start := 0; start < len(servers); start += p.batchSize {
		end := start + p.batchSize
		if end > len(servers) {
			end = len(servers)
		}
		batch := servers[start:end]
		if err := p.publisher.PublishServers(ctx, tenantID, batch); err != nil {
			return published, fmt.Errorf("publish: %w", err)
		}
		published += len(batch)
	}
	return published, nil
}

func (p *PlatformProjector) reportError(err error) {
	if err == nil {
		return
	}
	if p.onError != nil {
		p.onError(err)
		return
	}
	p.logger.Warn("serverregistry: platform projection", "error", err)
}
