package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	agentpkg "github.com/kombifyio/techstack/pkg/agent"
	"github.com/kombifyio/techstack/pkg/agent/httpguard"
	"github.com/kombifyio/techstack/pkg/agent/journal"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/guardexec"
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

// householdObserver reads the household identities of the node workspace
// through the pinned StackKits CLI (`stackkit user list`), read-only.
type householdObserver interface {
	HouseholdList(ctx context.Context) (guardexec.Observation, error)
}

// stackKitReconciler is the observation oracle of the typed StackKits actions.
// It only reads: APPLY is checked with the pinned CLI's verify, an application
// connector with its verify, and the household identity mutations with the
// household list. Every other mutating action has no safe oracle and is
// escalated to an operator.
type stackKitReconciler struct {
	executor  currentStackKitCommandExecutor
	household householdObserver
}

func (r stackKitReconciler) Reconcile(ctx context.Context, request httpguard.ReconcileRequest) httpguard.ReconcileOutcome {
	switch request.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_PROJECT.String(),
		agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_REMOVE.String(),
		agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_UPDATE_APPS.String():
		return r.reconcileHousehold(ctx, request)
	}

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

// reconcileHousehold settles an interrupted household identity mutation after
// a fresh read of the node's household list. A list that cannot be read, or a
// command that did not run in the observed workspace, stays escalated.
//
//   - HOUSEHOLD_PROJECT resolves not_applied. A projection never repeats an
//     uncertain provider effect when it runs again: it is bound to its
//     owner/person key and a monotonic revision, persists that revision before
//     any provider call, keeps signed custody of the member's provider subject
//     and recovers an uncertain creation or deletion only by that subject's
//     readback. Re-issuing any revision is therefore safe, and the household
//     worker re-sends the current one. The list cannot prove a projection
//     applied (it shows no subject, key or revision), so nothing is claimed
//     converged from it.
//   - HOUSEHOLD_UPDATE_APPS and HOUSEHOLD_REMOVE act on a username. Without the
//     recorded username the outcome stays escalated. An app update sets the
//     whole grant: an observed grant equal to the desired one is converged,
//     anything else is not applied and safe to re-issue. A removal is converged
//     when the member is absent; a present member is not removed yet, and
//     removing it again is the command's own effect.
func (r stackKitReconciler) reconcileHousehold(ctx context.Context, request httpguard.ReconcileRequest) httpguard.ReconcileOutcome {
	escalate := func(reason string) httpguard.ReconcileOutcome {
		return httpguard.ReconcileOutcome{Resolution: guardbinding.ResolutionEscalated, Reason: reason}
	}
	if r.household == nil {
		return escalate("household_observation_unavailable")
	}
	observation, err := r.household.HouseholdList(ctx)
	if err != nil || !observation.Supported || observation.ExitCode != 0 || observation.Workspace == "" {
		return escalate("household_list_unavailable")
	}
	if filepath.Clean(strings.TrimSpace(request.Hint.WorkingDirectory)) != observation.Workspace {
		return escalate("household_command_workspace_not_observed")
	}
	members, ok := parseHouseholdList(observation.Stdout)
	if !ok {
		return escalate("household_list_unreadable")
	}
	if request.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_PROJECT.String() {
		return httpguard.ReconcileOutcome{Resolution: guardbinding.ResolutionNotApplied, Reason: "household_projection_custody_makes_reissue_safe"}
	}
	username := strings.TrimSpace(request.Hint.HouseholdUsername)
	if username == "" {
		return escalate("household_command_member_not_recorded")
	}
	member, present := members[username]
	converged := !present
	if request.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_UPDATE_APPS.String() {
		converged = present && householdAppsMatch(member.Apps, request.Hint.HouseholdAppsSet, request.Hint.HouseholdApps)
	}
	if converged {
		return httpguard.ReconcileOutcome{Resolution: guardbinding.ResolutionObservedConverged, Reason: "household_list_observed_desired_member_state"}
	}
	return httpguard.ReconcileOutcome{Resolution: guardbinding.ResolutionNotApplied, Reason: "household_desired_state_not_observed_reissue_converges"}
}

// householdMember is one row of `stackkit user list --json`. Apps is "all" or
// the list of granted apps.
type householdMember struct {
	Username string          `json:"username"`
	Apps     json.RawMessage `json:"apps"`
}

// parseHouseholdList reads the household list result. Anything but a
// successful result with a user list of distinct usernames is unreadable.
func parseHouseholdList(stdout []byte) (map[string]householdMember, bool) {
	var result struct {
		Status string `json:"status"`
		Data   *struct {
			Users []householdMember `json:"users"`
		} `json:"data"`
	}
	if json.Unmarshal(stdout, &result) != nil || result.Status != "success" || result.Data == nil || result.Data.Users == nil {
		return nil, false
	}
	members := make(map[string]householdMember, len(result.Data.Users))
	for _, member := range result.Data.Users {
		if _, duplicate := members[member.Username]; duplicate || strings.TrimSpace(member.Username) == "" {
			return nil, false
		}
		members[member.Username] = member
	}
	return members, true
}

func householdAppsMatch(observed json.RawMessage, set bool, desired []string) bool {
	var all string
	if json.Unmarshal(observed, &all) == nil {
		return all == "all" && !set
	}
	var apps []string
	if !set || json.Unmarshal(observed, &apps) != nil || apps == nil {
		return false
	}
	return slices.Equal(sortedCopy(apps), sortedCopy(desired))
}

func sortedCopy(values []string) []string {
	out := slices.Clone(values)
	if out == nil {
		out = []string{}
	}
	slices.Sort(out)
	return out
}
