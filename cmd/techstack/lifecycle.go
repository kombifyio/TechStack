package main

import (
	"context"
	"fmt"

	"github.com/pocketbase/pocketbase"

	"github.com/kombifyio/techstack/pkg/backup"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/drift"
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
// server, job orchestrator, backup/drift schedulers, monitoring) and returns
// the handles needed to stop them. It is the direct-call replacement for the
// previously OnServe-bound lifecycle binders.
func startRuntimeLifecycle(
	ctx context.Context,
	app *pocketbase.PocketBase,
	cfg *config.Config,
	orch *orchestrator.Orchestrator,
	grpcState *grpcBoot,
	monitorState *monitoringBoot,
	workflowEngine *workflow.Engine,
	rilSignalWorker *signals.Worker,
	providerRuntime providerRuntimeRunner,
	registrySweeper *serverregistry.Sweeper,
	jobReclaimer *orchestrator.JobExecutionReclaimer,
	log *logger.Logger,
) *shutdownHandles {
	handles := &shutdownHandles{}

	if grpcState.server != nil {
		go func() {
			// Use the runtime lifecycle context (not context.Background) so the
			// gRPC server's ctx-cancellation watcher triggers GracefulStop on
			// shutdown (see grpcserver.Server.Start). Resolves gosec G118.
			if err := grpcState.server.Start(ctx); err != nil {
				fmt.Printf("⚠️  gRPC server error: %v\n", err)
			}
		}()
		fmt.Printf("   gRPC:           %s (agent connections)\n", grpcState.addr)
	}

	orch.Start()
	fmt.Println("   Jobs:           Orchestrator started with 4 workers")

	if jobReclaimer != nil {
		// The first pass is the startup reconciliation: a job row still
		// 'running' behind an expired execution lease belongs to a boot that is
		// over, and it holds its stack's execution claim until it is
		// terminalized. The loop stops when ctx is canceled on shutdown.
		go jobReclaimer.Run(ctx)
		fmt.Println("   Job reclaim:    orphaned execution reconciliation started")
	}

	if cfg.Backup.Enabled {
		startBackupScheduler(app, cfg, handles)
	} else {
		fmt.Println("   Backups:        Scheduler disabled (set TECHSTACK_BACKUP_ENABLED=true to enable)")
	}

	if cfg.Drift.Enabled {
		schedulerCfg := drift.NewSchedulerConfigFromConfig(cfg)
		handles.driftScheduler = drift.NewScheduler(app, orch, schedulerCfg)
		handles.driftScheduler.Start()
		fmt.Printf("   Drift:          Scheduler started (interval: %s, retention: %d)\n", cfg.Drift.Interval, cfg.Drift.Retention)
	} else {
		fmt.Println("   Drift:          Scheduler disabled (set TECHSTACK_DRIFT_ENABLED=true to enable)")
	}

	handles.monitorCancel = startMonitoringRuntime(ctx, monitorState, log)

	if workflowEngine != nil {
		// The worker's poll + timer-sweep loops stop when ctx is canceled on
		// shutdown, so no explicit stop handle is needed.
		workflow.NewWorker(workflowEngine, workflow.DefaultWorkerConfig()).Start(ctx)
		fmt.Println("   Workflows:      RIL engine + worker started (Postgres-backed)")
	}

	if rilSignalWorker != nil {
		go rilSignalWorker.Run(ctx)
		fmt.Println("   RIL signals:    durable Gateway publisher started")
	}

	if registrySweeper != nil {
		// The sweeper's loop stops when ctx is canceled on shutdown, so no
		// explicit stop handle is needed.
		go registrySweeper.Run(ctx)
		fmt.Println("   Registry sweep: observation demotion + outbox retention started")
	}

	if providerRuntime != nil {
		handles.providerControl = startProviderControlLifecycle(ctx, providerRuntime.Run)
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
		orch.Stop,
		func() {
			if providerDatabase != nil {
				_ = providerDatabase.Close()
			}
		},
		func() {
			if handles.monitorCancel != nil {
				handles.monitorCancel()
			}
			if monitorState.tsdb != nil {
				_ = monitorState.tsdb.Close()
			}
			if handles.driftScheduler != nil {
				handles.driftScheduler.Stop()
			}
			if handles.backupScheduler != nil {
				handles.backupScheduler.Stop()
			}
			if tunnelResolver != nil {
				_ = tunnelResolver.Stop()
			}
			if grpcState.server != nil {
				_ = grpcState.server.Stop()
			}
		},
		func() {
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

func startBackupScheduler(app *pocketbase.PocketBase, cfg *config.Config, handles *shutdownHandles) {
	schedulerCfg := backup.NewSchedulerConfigFromConfig(cfg)
	scheduler, err := backup.NewScheduler(app, schedulerCfg)
	if err != nil {
		fmt.Printf("⚠️  Backup scheduler creation failed: %v\n", err)
		return
	}
	handles.backupScheduler = scheduler
	scheduler.Start()
	s3Status := "disabled"
	if schedulerCfg.S3Enabled {
		s3Status = "enabled"
	}
	fmt.Printf("   Backups:        Scheduler started (interval: %s, retention: %d, S3: %s)\n",
		cfg.Backup.Interval, cfg.Backup.Retention, s3Status)
}

func startMonitoringRuntime(parent context.Context, state *monitoringBoot, log *logger.Logger) context.CancelFunc {
	if state.tsdb == nil && state.remote == nil {
		return nil
	}
	monCtx, cancel := context.WithCancel(parent)
	if state.tsdb != nil {
		retSvc := monitoring.NewRetentionService(state.tsdb, monitoring.RetentionConfig{Logger: log.Logger})
		go retSvc.Run(monCtx)
	}
	if state.alertEngine != nil {
		go state.alertEngine.Run(monCtx)
	}
	if state.notifyOutbox != nil {
		go state.notifyOutbox.Run(monCtx)
	}
	printMonitoringStartup(state)
	return cancel
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
