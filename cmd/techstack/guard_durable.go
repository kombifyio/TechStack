package main

import (
	"context"
	"log/slog"
	"os"
	"strings"

	agentpkg "github.com/kombifyio/techstack/pkg/agent"
	"github.com/kombifyio/techstack/pkg/agent/httpguard"
	"github.com/kombifyio/techstack/pkg/agent/journal"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// enableGuardDurableOperations switches the typed command path to the
// journaled, bound and acknowledged protocol.
// It shares the state directory that holds the Guard's host identity.
func enableGuardDurableOperations(root *os.Root, client *httpguard.Client, reconciler httpguard.Reconciler, log *slog.Logger) {
	client.EnableDurableOperations(httpguard.DurableOptions{
		CustodyRoot: root,
		Journal:     journal.Options{},
		Reconciler:  reconciler,
		ObserveProcess: func(ctx context.Context, record func(httpguard.ProcessRecord)) context.Context {
			return agentpkg.WithProcessObserver(ctx, func(p agentpkg.ProcessRef) {
				record(httpguard.ProcessRecord{PID: p.PID, PGID: p.PGID, StartTicks: p.StartTicks})
			})
		},
	})
	log.Info("guard_durable_operations_enabled", "state_dir", root.Name())
}

// stackKitReconciler is the observation oracle of the typed StackKits actions.
// It only reads: APPLY is checked with the pinned CLI's verify. Every other
// mutating action has no safe oracle and is escalated to an operator.
type stackKitReconciler struct {
	executor currentStackKitCommandExecutor
}

func (r stackKitReconciler) Reconcile(ctx context.Context, request httpguard.ReconcileRequest) httpguard.ReconcileOutcome {

	if request.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONNECTOR.String() {
		verify := &agentpb.StackKitCommand{CommandId: request.CommandID + "-reconcile", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONNECTOR_VERIFY, WorkingDirectory: request.Hint.WorkingDirectory, SpecPath: request.Hint.SpecPath, Release: request.Hint.Release, TimeoutSeconds: 300, StackkitInstanceId: request.Hint.StackkitInstanceID, ExpectedPlanHash: request.Hint.ExpectedPlanHash, ServiceKey: "smart-home", ApplicationOperationId: request.Hint.ConnectorOperationID, ApplicationConnectorRequestJson: request.Hint.ConnectorRequest, LocalSiteRef: request.Hint.LocalSiteRef, LocalNodeRef: request.Hint.LocalNodeRef, LocalExecutionChannelRef: request.Hint.LocalExecutionChannelRef}
		if err := stackkitcommand.ValidateCommand(verify); err != nil {
			return httpguard.ReconcileOutcome{Resolution: guardbinding.ResolutionEscalated, Reason: "connector_reconciliation_binding_unavailable"}
		}
		result := r.executor.Execute(ctx, verify)
		if stackkitcommand.ValidateResult(result, verify) == nil {
			if _, _, err := stackkitcommand.ParseApplicationConnectorEvidence(verify, result); err == nil {
				return httpguard.ReconcileOutcome{Resolution: guardbinding.ResolutionObservedConverged, Reason: "connector_current_read_verified_original_operation"}
			}
		}
		return httpguard.ReconcileOutcome{Resolution: guardbinding.ResolutionEscalated, Reason: "connector_current_read_did_not_verify_original_operation"}
	}
	if request.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY.String() ||
		request.Hint.Release == nil || strings.TrimSpace(request.Hint.WorkingDirectory) == "" {
		return httpguard.ReconcileOutcome{Resolution: guardbinding.ResolutionEscalated, Reason: "no_reconciliation_oracle_for_action"}
	}
	verify := &agentpb.StackKitCommand{
		CommandId:        request.CommandID + "-reconcile",
		Operation:        agentpb.StackKitOperation_STACKKIT_OPERATION_VERIFY,
		WorkingDirectory: request.Hint.WorkingDirectory,
		SpecPath:         request.Hint.SpecPath,
		Release:          request.Hint.Release,
		Offline:          request.Hint.Offline,
		TimeoutSeconds:   300,
	}
	result := r.executor.Execute(ctx, verify)
	if result != nil && result.Success {
		return httpguard.ReconcileOutcome{Resolution: guardbinding.ResolutionObservedConverged, Reason: "verify_observed_desired_state"}
	}
	return httpguard.ReconcileOutcome{Resolution: guardbinding.ResolutionEscalated, Reason: "verify_did_not_observe_desired_state"}
}
