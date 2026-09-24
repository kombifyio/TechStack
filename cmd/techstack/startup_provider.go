package main

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
)

// Both builds run the same native authority under its restricted database role.
// The public composition wrapper registers only the customer's Proxmox adapter.
func init() {
	runHostedProviderControlBootstrap = runProviderControlRuntimeBootstrap
	composeHostedProviderRuntime = func(
		ctx context.Context,
		database *sql.DB,
		edition config.Edition,
		log *logger.Logger,
		notificationOutbox *notifications.Outbox,
		orch *orchestrator.Orchestrator,
		workflowEngine *workflow.Engine,
	) (providerRuntimeState, error) {
		providerDatabase, providerDatabaseErr := openProviderControlRuntimeDatabase(ctx, database)
		if providerDatabaseErr != nil {
			// Provider Control is an independently gated product surface. An absent
			// or unsafe runtime role disables that surface without taking unrelated
			// self-hosted control-plane APIs down.
			log.Warn("provider_control_disabled", "reason", "dedicated_runtime_database_unavailable_or_posture_invalid")
			return providerRuntimeState{}, nil
		}

		providerRuntime, providerActions, err := composeProviderControl(
			providerDatabase.DB,
			edition,
			log,
			notificationOutbox,
			func(operation providercontrol.OperationRef) {
				if recoveryErr := orch.RehydrateProviderProvisionWait(ctx, operation); recoveryErr != nil {
					log.Warn("provider_provision_wait_rehydration_failed",
						"tenant", operation.TenantID, "operation", operation.OperationID, "error", recoveryErr)
				}
			},
			workflowEngine,
		)
		if err != nil {
			_ = providerDatabase.Close()
			return providerRuntimeState{}, err
		}

		// Queue timers are process-local. Keep provider-decommission wait
		// recovery inside the same activation-gated runtime.
		runtimeForRecovery := providerRuntime
		if err := runtimeForRecovery.RegisterAuxiliaryWorker(func(recoveryCtx context.Context) {
			orch.RunDueProviderDecommissionRecovery(recoveryCtx, runtimeForRecovery.Ledger())
		}); err != nil {
			_ = providerDatabase.Close()
			return providerRuntimeState{}, fmt.Errorf("register provider-decommission wait recovery: %w", err)
		}
		if err := runtimeForRecovery.RegisterAuxiliaryWorker(func(recoveryCtx context.Context) {
			orch.RunProviderProvisionRecovery(recoveryCtx, runtimeForRecovery.Ledger())
		}); err != nil {
			_ = providerDatabase.Close()
			return providerRuntimeState{}, fmt.Errorf("register provider-provision wait recovery: %w", err)
		}

		return providerRuntimeState{
			database:        providerDatabase,
			runner:          providerRuntime,
			actions:         providerActions,
			cleanupReadback: providerRuntime,
			resolution:      providerRuntime,
		}, nil
	}
}
