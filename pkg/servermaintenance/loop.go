package servermaintenance

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

const (
	defaultLoopInterval   = 15 * time.Second
	loopTenantPageSize    = 100
	loopJobPageSize       = 200
	loopMaxConcurrentJobs = 8
	loopShutdownDrain     = 10 * time.Second
)

// Loop advances every active maintenance job. A step that talks to an agent
// can block for as long as an apply runs (up to the agent's 25 minute
// budget), so each job runs in its own goroutine, at most one step per job at
// a time and at most loopMaxConcurrentJobs agent steps in parallel. A full
// slot pool skips the job until the next tick instead of stalling the sweep,
// and cheap steps (reboot watch, expiry, a poll that is not due) never wait
// for a slot. Replicas may run the loop side by side: every transition is a
// compare-and-set, a claim re-checks the node under its advisory lock, and
// re-attaching to a sent command only reads its durable outcome.
//
// Steps do not share the loop's context: on shutdown they get
// loopShutdownDrain to finish, then they are cancelled. A cancelled step never
// fails a job whose command was sent; the next process re-attaches to it.
type Loop struct {
	store    controlplane.ServerMaintenanceSweepStore
	runner   Runner
	interval time.Duration
	log      *slog.Logger
	inflight sync.Map
	slots    chan struct{}
	wg       sync.WaitGroup
	stepCtx  context.Context
}

// NewLoop returns nil without a store or dispatcher, which keeps the loop off.
func NewLoop(store controlplane.ServerMaintenanceSweepStore, runner Runner, interval time.Duration, log *slog.Logger) *Loop {
	if store == nil || runner.Jobs == nil || runner.Dispatcher == nil {
		return nil
	}
	if interval <= 0 {
		interval = defaultLoopInterval
	}
	if log == nil {
		log = slog.Default()
	}
	return &Loop{
		store: store, runner: runner, interval: interval, log: log,
		slots: make(chan struct{}, loopMaxConcurrentJobs), stepCtx: context.Background(),
	}
}

// Run sweeps immediately and then on every interval until ctx ends, and
// waits for running steps before it returns.
func (l *Loop) Run(ctx context.Context) {
	if l == nil {
		return
	}
	stepCtx, cancelSteps := context.WithCancel(context.WithoutCancel(ctx))
	l.stepCtx = stepCtx
	defer func() {
		drained := make(chan struct{})
		go func() { l.wg.Wait(); close(drained) }()
		select {
		case <-drained:
		case <-time.After(loopShutdownDrain):
			cancelSteps()
			<-drained
		}
		cancelSteps()
	}()
	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()
	for {
		l.Sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Sweep starts one step for every active job that is not already stepping.
func (l *Loop) Sweep(ctx context.Context) {
	after := ""
	for ctx.Err() == nil {
		tenants, err := l.store.ListServerMaintenanceTenants(ctx, after, loopTenantPageSize)
		if err != nil {
			l.log.Warn("server_maintenance_sweep_tenants_failed", "error", err.Error())
			return
		}
		for _, tenantID := range tenants {
			l.sweepTenant(ctx, tenantID)
		}
		if len(tenants) < loopTenantPageSize {
			return
		}
		after = tenants[len(tenants)-1]
	}
}

func (l *Loop) sweepTenant(ctx context.Context, tenantID string) {
	jobs, err := l.store.ListActiveServerMaintenanceJobs(ctx, tenantID, loopJobPageSize)
	if err != nil {
		l.log.Warn("server_maintenance_sweep_jobs_failed", "tenant_id", tenantID, "error", err.Error())
		return
	}
	if len(jobs) == 0 {
		if err := l.store.CompactServerMaintenanceTenant(ctx, tenantID); err != nil {
			l.log.Warn("server_maintenance_compact_failed", "tenant_id", tenantID, "error", err.Error())
		}
		return
	}
	for _, job := range jobs {
		key := job.TenantID + "\x00" + job.ID
		if _, busy := l.inflight.LoadOrStore(key, struct{}{}); busy {
			continue
		}
		slotted := l.runner.NeedsDispatchSlot(job)
		if slotted {
			select {
			case l.slots <- struct{}{}:
			default:
				// Every slot is busy: try again on the next tick.
				l.inflight.Delete(key)
				continue
			}
		}
		l.wg.Add(1)
		go func(job controlplane.ServerMaintenanceJob) {
			defer func() {
				if slotted {
					<-l.slots
				}
				l.inflight.Delete(key)
				l.wg.Done()
			}()
			next, err := l.runner.Advance(l.stepCtx, job)
			if err != nil {
				l.log.Warn("server_maintenance_step_failed", "tenant_id", job.TenantID, "job_id", job.ID, "state", job.State, "error", err.Error())
				return
			}
			if next != nil && next.State != job.State {
				l.log.Info("server_maintenance_step", "tenant_id", job.TenantID, "job_id", job.ID, "action", job.Action,
					"from", job.State, "to", next.State, "reason_code", next.ReasonCode)
			}
		}(job)
	}
}
