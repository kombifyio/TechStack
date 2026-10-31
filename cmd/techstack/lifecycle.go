package main

import (
	"context"
	"fmt"
	"github.com/kombifyio/techstack/pkg/servermaintenance"
	"sync"

	"github.com/kombifyio/techstack/internal/backupjobs"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/guardreach"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/ril/signals"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/tunnel"
)

type providerControlLifecycle struct {
	cancel context.CancelFunc
	done   <-chan struct{}
}

type backgroundRuntimeLifecycle struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newBackgroundRuntimeLifecycle(parent context.Context) *backgroundRuntimeLifecycle {
	ctx, cancel := context.WithCancel(parent)
	return &backgroundRuntimeLifecycle{ctx: ctx, cancel: cancel}
}

func (lifecycle *backgroundRuntimeLifecycle) run(run func(context.Context)) {
	if lifecycle == nil || run == nil {
		return
	}
	lifecycle.wg.Add(1)
	go func() {
		defer lifecycle.wg.Done()
		run(lifecycle.ctx)
	}()
}

func (lifecycle *backgroundRuntimeLifecycle) stopAndWait() {
	if lifecycle == nil {
		return
	}
	lifecycle.cancel()
	lifecycle.wg.Wait()
}

// providerRuntimeRunner keeps the lifecycle boundary provider-neutral. The
// hosted provider-control runtime satisfies this interface in the private
// build; self-hosted exports simply pass nil and do not carry provider
// authority into the public tree.
type providerRuntimeRunner interface {
	Run(context.Context)
}

func startProviderControlLifecycle(parent context.Context, run func(context.Context)) *providerControlLifecycle {
	if run == nil {
		return nil
	}
	runtimeCtx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(runtimeCtx)
	}()
	return &providerControlLifecycle{cancel: cancel, done: done}
}

func (lifecycle *providerControlLifecycle) stopAndWait() {
	if lifecycle == nil {
		return
	}
	lifecycle.cancel()
	<-lifecycle.done
}

// startRuntimeLifecycle starts the background runtime components (agent gRPC
// server, job orchestrator, drift scheduler, monitoring) and returns
// the handles needed to stop them. It is the direct-call replacement for the
// previously OnServe-bound lifecycle binders.
//
// Both release channels start the homelab work; each of those loops skips the
// tenants the other channel owns (pkg/releasechannel). Shared work that is
// nobody's homelab (agent connections, RIL workflows and signals, Guard reach,
// registry sweeps, the platform projection) runs only on stable.
func startRuntimeLifecycle(
	ctx context.Context,
	cfg *config.Config,
	orch *orchestrator.Orchestrator,
	grpcState *grpcBoot,
	monitorState *monitoringBoot,
	workflowEngine *workflow.Engine,
	rilSignalWorker *signals.Worker,
	guardReach *guardreach.Service,
	providerRuntime providerRuntimeRunner,
	registrySweeper *serverregistry.Sweeper,
	platformProjector *serverregistry.PlatformProjector,
	jobReclaimer *orchestrator.JobExecutionReclaimer,
	backupScanner *backupjobs.Scanner,
	backupRetention *backupRetention,
	serverMaintenance *servermaintenance.Loop,
	driftScheduler *orchestrator.DriftScheduler,
	log *logger.Logger,
	secondaryExecution ...*config.SecondaryExecution,
) *shutdownHandles {
	background := newBackgroundRuntimeLifecycle(ctx)
	handles := &shutdownHandles{background: background}
	shared := config.RunsSharedWork()
	if !shared {
		for _, execution := range secondaryExecution {
			execution.BackgroundPartitioned()
		}
		log.Info("preview_background_execution_partitioned", "scope", "owned-tenants")
	}

	if grpcState.server != nil {
		background.run(func(ctx context.Context) {
			// Use the runtime lifecycle context (not context.Background) so the
			// gRPC server's ctx-cancellation watcher triggers GracefulStop on
			// shutdown (see grpcserver.Server.Start). Resolves gosec G118.
			if err := grpcState.server.Start(ctx); err != nil {
				fmt.Printf("⚠️  gRPC server error: %v\n", err)
			}
		})
		fmt.Printf("   gRPC:           %s (agent connections)\n", grpcState.addr)
	}

	orch.Start()
	fmt.Println("   Jobs:           Orchestrator started with 4 workers")

	if serverMaintenance != nil {
		background.run(serverMaintenance.Run)
		fmt.Println("   Maintenance:    Server maintenance runner started")
	}

	if backupScanner != nil {
		// Bound to the runtime lifecycle context so a shutdown stops the pass
		// instead of leaving a loop enqueueing against a closing queue.
		background.run(backupScanner.Run)
		fmt.Println("   Backups:        Due-stack scanner started")
	}

	if backupRetention != nil {
		background.run(backupRetention.Run)
		fmt.Println("   Backups:        Retention wipe sweeper started")
	}

	// A crash between the durable job insert and the queue enqueue stranded
	// the run until the user retried. Re-admit recent pending jobs whose
	// recovery payload survived redaction; the per-stack execution claim keeps
	// this safe across replicas.
	if requeued, requeueErr := orch.RequeuePendingJobs(ctx); requeueErr != nil {
		log.Warn("pending_jobs_requeue_failed", "error", requeueErr, "requeued", requeued)
	}

	if jobReclaimer != nil {
		// The first pass is the startup reconciliation: a job row still
		// 'running' behind an expired execution lease belongs to a boot that is
		// over, and it holds its stack's execution claim until it is
		// terminalized. The loop stops when ctx is canceled on shutdown.
		background.run(jobReclaimer.Run)
		fmt.Println("   Job reclaim:    orphaned execution reconciliation started")
	}

	switch {
	case driftScheduler != nil:
		background.run(driftScheduler.Run)
		fmt.Printf("   Drift:          Advanced drift detection every %s\n", driftScheduler.Interval())
	case cfg.Drift.Enabled:
		fmt.Println("   Drift:          Scheduler unavailable without the control-plane database")
	default:
		fmt.Println("   Drift:          Scheduler disabled (TECHSTACK_DRIFT_ENABLED=false)")
	}

	startMonitoringRuntime(background, monitorState, log)

	if workflowEngine != nil && shared {
		background.run(workflow.NewWorker(workflowEngine, workflow.DefaultWorkerConfig()).Run)
		fmt.Println("   Workflows:      RIL engine + worker started (Postgres-backed)")
	}

	if rilSignalWorker != nil && shared {
		background.run(rilSignalWorker.Run)
		fmt.Println("   RIL signals:    durable Gateway publisher started")
	}

	if guardReach != nil && shared {
		background.run(guardReach.Run)
		fmt.Println("   Guard reach:    kombify.me relay binding + Connect resource reconciler started")
	}

	if registrySweeper != nil && shared {
		background.run(registrySweeper.Run)
		fmt.Println("   Registry sweep: observation demotion + outbox retention started")
	}

	if platformProjector != nil && shared {
		background.run(platformProjector.Run)
		fmt.Println("   Platform list: server inventory projected into kombify-db")
	}

	if providerRuntime != nil {
		handles.providerControl = startProviderControlLifecycle(background.ctx, providerRuntime.Run)
		fmt.Println("   ProviderControl: native reconciler composed (mutations activation-gated)")
	}

	return handles
}

// stopRuntimeLifecycle tears down the background runtime components and closes
// the control-plane database. It is the direct-call replacement for the
// previously OnTerminate-bound shutdown hook.
func stopRuntimeLifecycle(
	orch *orchestrator.Orchestrator,
	grpcState *grpcBoot,
	tunnelResolver *tunnel.RegistryURLResolver,
	monitorState *monitoringBoot,
	providerDatabase *db.DB,
	v2State *v2Boot,
	handles *shutdownHandles,
) {
	runRuntimeShutdownSequence(
		handles.providerControl,
		func() {
			orch.Stop()
			handles.background.stopAndWait()
		},
		func() {
			if providerDatabase != nil {
				_ = providerDatabase.Close()
			}
		},
		func() {
			if monitorState.tsdb != nil {
				_ = monitorState.tsdb.Close()
			}
			if tunnelResolver != nil {
				_ = tunnelResolver.Stop()
			}
			if grpcState.server != nil {
				_ = grpcState.server.Stop()
			}
		},
		func() {
			if v2State.advancedIssuerCustody != nil {
				_ = v2State.advancedIssuerCustody.Close()
			}
			if v2State.db != nil {
				_ = v2State.db.Close()
			}
		},
	)
}

// runRuntimeShutdownSequence makes the provider mutation boundary explicit:
// worker claims drain first, admission producers drain second, and only then
// may the dedicated runtime pool close. The retained control-plane/migration
// pool closes last, after every remaining runtime component has stopped.
func runRuntimeShutdownSequence(
	providerLifecycle *providerControlLifecycle,
	stopAdmissionProducers func(),
	closeProviderDatabase func(),
	stopRemainingRuntime func(),
	closeControlPlaneDatabase func(),
) {
	providerLifecycle.stopAndWait()
	if stopAdmissionProducers != nil {
		stopAdmissionProducers()
	}
	if closeProviderDatabase != nil {
		closeProviderDatabase()
	}
	if stopRemainingRuntime != nil {
		stopRemainingRuntime()
	}
	if closeControlPlaneDatabase != nil {
		closeControlPlaneDatabase()
	}
}

func startMonitoringRuntime(background *backgroundRuntimeLifecycle, state *monitoringBoot, log *logger.Logger) {
	if state.domainMonitor != nil {
		background.run(state.domainMonitor.Run)
	}
	if state.driveHealth != nil {
		background.run(state.driveHealth.Run)
	}
	if state.notifyOutbox != nil {
		background.run(state.notifyOutbox.Run)
	}
	if state.tsdb == nil && state.remote == nil {
		return
	}
	if state.tsdb != nil {
		retSvc := monitoring.NewRetentionService(state.tsdb, monitoring.RetentionConfig{Logger: log.Logger})
		background.run(retSvc.Run)
	}
	if state.alertEngine != nil {
		background.run(state.alertEngine.Run)
	}
	printMonitoringStartup(state)
}

func printMonitoringStartup(state *monitoringBoot) {
	if state.tsdb != nil && state.remote != nil {
		fmt.Printf("   Monitoring:     TSDB ingest active (%s), remote query backend %s, retention + alerts started\n", state.dataDir, state.remote.RedactedBaseURL())
		return
	}
	if state.tsdb != nil {
		fmt.Printf("   Monitoring:     TSDB active (%s), retention + alerts started\n", state.dataDir)
		return
	}
	fmt.Printf("   Monitoring:     remote query backend %s active (embedded TSDB unavailable)\n", state.remote.RedactedBaseURL())
}
