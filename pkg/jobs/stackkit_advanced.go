package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/internal/advancedissuer"
	"github.com/kombifyio/techstack/internal/runtimeproduct/runtimeaction"
	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// Internal step names of the Advanced lifecycle operations. They are not
// operator operations: the operator asks for advanced_change_set,
// drift_reconcile, rollback or restore_drill and Core dispatches the steps.
const (
	stackKitStepChangeSetCreate = "advanced_change_set_create"
	stackKitStepChangeSetApply  = "advanced_change_set_apply"
	stackKitStepDriftReconcile  = "advanced_drift_reconcile"
	stackKitStepRollback        = "advanced_rollback"
	stackKitStepRestoreDrill    = "advanced_restore_drill"

	// StackKitModeAdvanced is the stackkit_mode every managed deployment
	// records: Techstack dispatches Advanced Mode only.
	StackKitModeAdvanced = "advanced"
	// StackKitModeField is the deploy-result and stack-config mode key.
	StackKitModeField = "stackkit_mode"

	advancedResultField     = "advanced_result"
	advancedDenialField     = "advanced_denial"
	stackKitInstanceIDField = "stackkit_instance_id"
	resultModeField         = "mode"
	resultPlanHashField     = "plan_hash"
	resultSnapshotAnchorKey = "snapshot_anchor_id"
	resultActionField       = "action"
	driftStackRoleField     = "role"
	advancedDenialDetailKey = "detail"
	advancedOperationField  = "operation"
	advancedReasonCodeField = "reason_code"
)

// IsAdvancedStackKitLifecycleOperation reports the operator operations Core
// runs as capability-gated Advanced operations.
func IsAdvancedStackKitLifecycleOperation(operation string) bool {
	switch strings.ToLower(strings.TrimSpace(operation)) {
	case StackKitLifecycleAdvancedChangeSet, StackKitLifecycleDriftReconcile,
		StackKitLifecycleRollback, StackKitLifecycleRestoreDrill:
		return true
	default:
		return false
	}
}

// AdvancedOperationDeniedError is a StackKits admission denial
// (stackkit.operation-denial/v1). The job fails with its reason code.
type AdvancedOperationDeniedError struct {
	Operation  string
	ReasonCode string
	Message    string
}

func (e *AdvancedOperationDeniedError) Error() string {
	return fmt.Sprintf("StackKits denied Advanced operation %s: %s", e.Operation, e.ReasonCode)
}

// AdvancedOperationUnavailableError reports an operation the pinned
// release's catalog does not list as available. Core refuses to dispatch it.
type AdvancedOperationUnavailableError struct {
	Operation string
	Err       error
}

func (e *AdvancedOperationUnavailableError) Error() string {
	return fmt.Sprintf("Advanced operation %s is not dispatchable: %v", e.Operation, e.Err)
}

func (e *AdvancedOperationUnavailableError) Unwrap() error { return e.Err }

// advancedFailureReason maps an Advanced failure to its structured reason.
func advancedFailureReason(err error) (string, string, bool) {
	var denied *AdvancedOperationDeniedError
	if errors.As(err, &denied) {
		return denied.ReasonCode, denied.Operation, true
	}
	var unavailable *AdvancedOperationUnavailableError
	if errors.As(err, &unavailable) {
		return "advanced_operation_unavailable", unavailable.Operation, true
	}
	return "", "", false
}

// advancedDispatcher mints one capability per operator Advanced operation and
// sends it with each typed command of that operation. Capabilities are scoped
// to the deployment's StackKit stack id and the local Owner of its trust
// binding. StackKits binds a change set to the capability that created it
// (the record stores capabilityId and its digest, and apply or Advanced
// reconcile reject any other capability), so a change-set flow carries one
// capability allowing exactly its create and its apply or reconcile step.
type advancedDispatcher struct {
	sender   StackKitCommandSender
	issuer   AdvancedIssuer
	release  stackkitrelease.Release
	catalog  stackkitrelease.AdvancedOperationsCatalog
	binding  advancedissuer.TrustBinding
	stackID  string
	request  StackKitLifecycleRequest
	commands []*agentpb.StackKitCommand
}

func newAdvancedDispatcher(ctx context.Context, sender StackKitCommandSender, issuer AdvancedIssuer, release stackkitrelease.Release, request StackKitLifecycleRequest) (*advancedDispatcher, error) {
	if sender == nil {
		return nil, fmt.Errorf("typed StackKits dispatcher is not configured")
	}
	if issuer == nil {
		return nil, advancedissuer.ErrUnavailable
	}
	catalog, err := release.AdvancedOperations()
	if err != nil {
		return nil, err
	}
	binding, err := issuer.TrustBindingFor(ctx, request.TenantID, request.StackID)
	if err != nil {
		return nil, fmt.Errorf("managed deployment has no Advanced trust binding; run advanced_trust_import: %w", err)
	}
	if request.BackupRenewal != nil && binding.StackID != request.StackKitInstanceID {
		return nil, fmt.Errorf("backup renewal differs from enrolled Advanced instance")
	}
	stackID := firstNonEmpty(binding.StackID, request.StackKitInstanceID)
	if stackID == "" {
		return nil, fmt.Errorf("managed deployment has no StackKit stack id for its Advanced capability")
	}
	return &advancedDispatcher{
		sender: sender, issuer: issuer, release: release, catalog: catalog,
		binding: binding, stackID: stackID, request: request,
	}, nil
}

// require refuses every step the pinned release's catalog does not list as
// available before the first step is dispatched.
func (d *advancedDispatcher) require(steps ...string) ([]string, error) {
	operations := make([]string, 0, len(steps))
	for _, step := range steps {
		operation, err := stackKitLifecycleAgentOperation(step)
		if err != nil {
			return nil, err
		}
		catalogOperation := stackkitcommand.AdvancedCatalogOperation(operation)
		if dispatchErr := d.catalog.Dispatchable(catalogOperation); dispatchErr != nil {
			return nil, &AdvancedOperationUnavailableError{Operation: catalogOperation, Err: dispatchErr}
		}
		operations = append(operations, catalogOperation)
	}
	return operations, nil
}

// issue mints the one capability of an operator operation.
func (d *advancedDispatcher) issue(ctx context.Context, operations []string) (advancedissuer.Capability, error) {
	capability, err := d.issuer.Issue(ctx, advancedissuer.Request{
		TenantID: d.request.TenantID, DeploymentID: d.request.StackID, StackID: d.stackID,
		OwnerRef: d.binding.OwnerRef, Operations: operations, TTL: advancedissuer.DefaultTTL, BackupRenewal: d.request.BackupRenewal,
	})
	if err != nil {
		return advancedissuer.Capability{}, fmt.Errorf("issue Advanced capability for %s: %w", strings.Join(operations, ", "), err)
	}
	return capability, nil
}

// dispatch sends one Advanced step with the operator operation's capability
// and admits the typed evidence.
func (d *advancedDispatcher) dispatch(ctx context.Context, commandID, step string, capability advancedissuer.Capability, prepare func(*agentpb.StackKitCommand)) (stackkitcommand.AdvancedOperationResult, map[string]interface{}, error) {
	request := d.request
	request.Operation = step
	command, err := stackKitLifecycleCommand(commandID, request, d.release)
	if err != nil {
		return stackkitcommand.AdvancedOperationResult{}, nil, err
	}
	catalogOperation := stackkitcommand.AdvancedCatalogOperation(command.Operation)
	if dispatchErr := d.catalog.Dispatchable(catalogOperation); dispatchErr != nil {
		return stackkitcommand.AdvancedOperationResult{}, nil, &AdvancedOperationUnavailableError{Operation: catalogOperation, Err: dispatchErr}
	}
	if prepare != nil {
		prepare(command)
	}
	command.AdvancedCapability = capability.Raw
	if validateErr := stackkitcommand.ValidateCommand(command); validateErr != nil {
		return stackkitcommand.AdvancedOperationResult{}, nil, fmt.Errorf("build Advanced %s command: %w", catalogOperation, validateErr)
	}
	d.commands = append(d.commands, command)
	result, err := sendStackKitCommandBoundedForTenant(ctx, d.sender, d.request.TenantID, d.request.AgentID, command)
	if err != nil {
		return stackkitcommand.AdvancedOperationResult{}, nil, &typedStackKitOperationError{Operation: step, CommandID: commandID, Err: err}
	}
	normalized, err := normalizeStackKitLifecycleResult(request, result)
	if err != nil {
		return stackkitcommand.AdvancedOperationResult{}, nil, err
	}
	normalized["capability_id"] = capability.ID
	if !result.Success {
		if denial, denied := stackkitcommand.ParseOperationDenial(result); denied {
			normalized[advancedDenialField] = map[string]interface{}{
				advancedReasonCodeField: denial.ReasonCode, advancedOperationField: firstNonEmpty(denial.Operation, catalogOperation), advancedDenialDetailKey: denial.Message,
			}
			return stackkitcommand.AdvancedOperationResult{}, normalized, &AdvancedOperationDeniedError{
				Operation: catalogOperation, ReasonCode: denial.ReasonCode, Message: denial.Message,
			}
		}
		return stackkitcommand.AdvancedOperationResult{}, normalized, fmt.Errorf("StackKits %s failed with exit code %d: %s", catalogOperation, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	admitted, err := stackkitcommand.ParseAdvancedOperationResult(command, result)
	if err != nil {
		return stackkitcommand.AdvancedOperationResult{}, normalized, err
	}
	if d.request.BackupRenewal != nil {
		if err := verifyManagedDrillReceipt(admitted.Report, d.request.BackupRenewal, commandID, capability.ID); err != nil {
			return stackkitcommand.AdvancedOperationResult{}, normalized, err
		}
	}
	normalized[advancedResultField] = advancedResultMap(admitted)
	return admitted, normalized, nil
}

func advancedResultMap(result stackkitcommand.AdvancedOperationResult) map[string]interface{} {
	out := map[string]interface{}{advancedOperationField: result.Operation, resultStatusField: result.Status}
	if result.ChangeSetID != "" {
		out["change_set_id"] = result.ChangeSetID
		out["change_set_sha256"] = result.ChangeSetSHA256
	}
	if len(result.Report) > 0 {
		var report interface{}
		if json.Unmarshal(result.Report, &report) == nil {
			out["report"] = report
		}
	}
	return out
}

// runAdvancedStackKitLifecycle runs one operator Advanced operation:
// advanced_change_set creates and applies a change set for the candidate
// StackSpec; drift_reconcile creates a change set for the candidate (the
// workspace StackSpec when none is given) and reconciles through it;
// rollback and restore_drill are one step each.
func runAdvancedStackKitLifecycle(ctx context.Context, cfg StackKitLifecycleConfig, release stackkitrelease.Release, jobID string, request StackKitLifecycleRequest) (map[string]interface{}, error) {
	dispatcher, err := newAdvancedDispatcher(ctx, cfg.Sender, cfg.AdvancedIssuer, release, request)
	if err != nil {
		return nil, err
	}
	steps := map[string][]string{
		StackKitLifecycleAdvancedChangeSet: {stackKitStepChangeSetCreate, stackKitStepChangeSetApply},
		StackKitLifecycleDriftReconcile:    {stackKitStepChangeSetCreate, stackKitStepDriftReconcile},
		StackKitLifecycleRollback:          {stackKitStepRollback},
		StackKitLifecycleRestoreDrill:      {stackKitStepRestoreDrill},
	}[request.Operation]
	if len(steps) == 0 {
		return nil, fmt.Errorf("unsupported Advanced lifecycle operation %q", request.Operation)
	}
	operations, err := dispatcher.require(steps...)
	if err != nil {
		return nil, err
	}
	capability, err := dispatcher.issue(ctx, operations)
	if err != nil {
		return nil, err
	}
	summary := map[string]interface{}{
		"schema_version": "techstack.stackkit-lifecycle-result/v1", "techstack_id": request.StackID,
		stackKitInstanceIDField: request.StackKitInstanceID, advancedOperationField: request.Operation, "agent_id": request.AgentID,
		StackKitModeField: StackKitModeAdvanced,
		"capability_id":   capability.ID,
	}
	if request.Operation == StackKitLifecycleAdvancedChangeSet {
		reconciliation, err := reconcileManagedChangeSetAddresses(ctx, cfg.ManagedAddressAuthority, release, request)
		if err != nil {
			summary["success"] = false
			return summary, err
		}
		summary[ManagedAddressReconciliationResultField] = reconciliation
	}
	var created stackkitcommand.AdvancedOperationResult
	stepResults := make([]interface{}, 0, len(steps))
	for index, step := range steps {
		commandID := jobID
		if len(steps) > 1 {
			commandID = jobID + "-" + step
		}
		admitted, normalized, stepErr := dispatcher.dispatch(ctx, commandID, step, capability, func(command *agentpb.StackKitCommand) {
			if index > 0 {
				command.ChangeSetId, command.ChangeSetSha256 = created.ChangeSetID, created.ChangeSetSHA256
			}
		})
		if normalized != nil {
			stepResults = append(stepResults, normalized)
			summary["steps"] = stepResults
			for _, key := range []string{advancedResultField, advancedDenialField, "capability_id", "command_result", "events", "release", "stderr", "exit_code"} {
				if value, ok := normalized[key]; ok {
					summary[key] = value
				}
			}
		}
		if stepErr != nil {
			summary["success"] = false
			return summary, stepErr
		}
		if step == stackKitStepChangeSetCreate {
			created = admitted
			summary["change_set_id"], summary["change_set_sha256"] = admitted.ChangeSetID, admitted.ChangeSetSHA256
		}
	}
	summary["success"] = true
	return summary, nil
}

// advancedGenerationCandidate sets generation.target terramate on the
// managed rollout's candidate StackSpec when the pinned release ships the
// Advanced operations catalog. Releases before the catalog do not allow the
// Terramate target for every kit (v0.46.1 allows only compose for Cloud Kit)
// and lack the Terramate executor, so their rollouts keep the kit's own
// target until the next repin.
func advancedGenerationCandidate(candidate []byte, release stackkitrelease.Release) ([]byte, bool, error) {
	catalog, err := release.AdvancedOperations()
	if err != nil {
		return nil, false, err
	}
	if catalog.Source != stackkitrelease.AdvancedOperationsSourceRelease {
		return candidate, false, nil
	}
	var document map[string]interface{}
	if decodeErr := json.Unmarshal(candidate, &document); decodeErr != nil {
		return nil, false, fmt.Errorf("decode managed rollout candidate: %w", decodeErr)
	}
	generation := mapFromInterface(document["generation"])
	if generation == nil {
		generation = map[string]interface{}{}
	}
	generation["target"] = "terramate"
	document["generation"] = generation
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, false, fmt.Errorf("encode managed rollout candidate: %w", err)
	}
	return encoded, true, nil
}

// runAdvancedStackKitLifecycleJob runs an operator Advanced operation as a
// lifecycle job. A restore drill first configures the owner-bound backup
// repository for the applied plan, which the drill's snapshot needs.
func runAdvancedStackKitLifecycleJob(ctx context.Context, cfg StackKitLifecycleConfig, job *Job, q *Queue, req StackKitLifecycleRequest, release stackkitrelease.Release) error {
	var managedAdmission map[string]interface{}
	if req.Operation == StackKitLifecycleRestoreDrill && req.StackKit != DefaultBasementKitRef {
		var err error
		req, managedAdmission, err = prepareManagedRestore(ctx, cfg, job, req, release)
		if err != nil {
			return NewPermanentError(err)
		}
	}
	if req.Operation == StackKitLifecycleRestoreDrill && req.BackupRenewal == nil {
		job.setStep("stackkit_backup_configure")
		q.UpdateProgress(job.ID, 20, "Configuring the owner-bound backup repository for the restore drill")
		err := configureStackKitBackupForDrill(ctx, cfg.Sender, job.ID, req, release)
		if err != nil {
			return NewPermanentError(err)
		}
	}
	job.setStep("stackkit_advanced_" + req.Operation)
	q.UpdateProgress(job.ID, 40, "Dispatching StackKits Advanced "+req.Operation)
	summary, err := runAdvancedStackKitLifecycle(ctx, cfg, release, job.ID, req)
	if summary != nil {
		if managedAdmission != nil {
			summary[ManagedBackupAdmissionField] = managedAdmission
		}
		job.replaceResult(summary)
	}
	if err != nil {
		if errors.Is(err, advancedissuer.ErrUnavailable) {
			return NewConfigError(err)
		}
		return NewPermanentError(err)
	}
	q.UpdateProgress(job.ID, 100, "StackKits Advanced "+req.Operation+" completed")
	return nil
}

func configureStackKitBackupForDrill(ctx context.Context, sender StackKitCommandSender, jobID string, req StackKitLifecycleRequest, release stackkitrelease.Release) error {
	configure := req
	configure.Operation = StackKitLifecycleBackupConfigure
	planHash := ""
	if configure.StackKitInstanceID != "" {
		var err error
		if planHash, err = planStackKitLifecycleApply(ctx, sender, jobID, configure, release); err != nil {
			return fmt.Errorf("bind restore drill to the node plan: %w", err)
		}
	}
	command, err := stackKitLifecycleCommand(jobID+"-backup_configure", configure, release)
	if err != nil {
		return err
	}
	command.ExpectedPlanHash = planHash
	result, err := sendStackKitCommandBoundedForTenant(ctx, sender, req.TenantID, req.AgentID, command)
	if err != nil {
		return fmt.Errorf("restore-drill backup configuration dispatch failed: %w", err)
	}
	if err := requireSuccessfulNativeBackupStep("backup configure", result); err != nil {
		return err
	}
	return stackkitcommand.ParseBackupConfigurationEvidence(command, result)
}

// driftReportMap projects a stackkit.drift-report/v1 for job results: the
// combined status and one entry per Terramate stack.
func driftReportMap(report stackkitcommand.DriftReport) map[string]interface{} {
	stacks := make([]interface{}, 0, len(report.Stacks))
	for _, stack := range report.Stacks {
		entry := map[string]interface{}{
			stackIDField: stack.StackID, driftStackRoleField: stack.Role, "module_ref": stack.ModuleRef,
			resultStatusField: stack.Status, "runtime_root": stack.RuntimeRoot,
		}
		if stack.PlanExit != nil {
			entry["plan_exit_code"] = *stack.PlanExit
		}
		stacks = append(stacks, entry)
	}
	return map[string]interface{}{
		resultStatusField: report.Status, resultModeField: report.Mode, "generation_target": report.GenerationTarget,
		"has_drift": report.HasDrift, "stacks": stacks,
	}
}

// runAdvancedRestoreDrill runs the managed rollout's restore drill as the
// Advanced restore.drill operation when the pinned release offers it. It
// reports handled false when the catalog lacks an available restore.drill,
// so the caller keeps the native backup run and staged restore sequence as
// the explicit fallback for releases before the drill shipped.
func (r *deployRollout) runAdvancedRestoreDrill(ctx context.Context, request StackKitLifecycleRequest, release stackkitrelease.Release, planHash string) (map[string]interface{}, bool, error) {
	if r.cfg.AdvancedIssuer == nil {
		return nil, false, nil
	}
	catalog, err := release.AdvancedOperations()
	if err != nil {
		return nil, true, err
	}
	if catalog.Dispatchable(stackkitcommand.AdvancedCatalogOperation(agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_RESTORE_DRILL)) != nil {
		return nil, false, nil
	}
	request.Operation = StackKitLifecycleRestoreDrill
	request.OwnerApproved = true
	summary, err := runAdvancedStackKitLifecycle(ctx, StackKitLifecycleConfig{Sender: r.cfg.StackKitCommander, AdvancedIssuer: r.cfg.AdvancedIssuer}, release, r.job.ID+"-restore-drill", request)
	if err != nil {
		return nil, true, err
	}
	advanced, _ := summary[advancedResultField].(map[string]interface{})
	report, _ := advanced["report"].(map[string]interface{})
	return map[string]interface{}{
		resultActionField:       string(runtimeaction.ActionRestoreDrill),
		resultStatusField:       string(runtimeaction.StatusVerified),
		resultModeField:         "advanced-restore-drill",
		stackKitInstanceIDField: request.StackKitInstanceID,
		resultPlanHashField:     planHash,
		"drill_id":              report["drillId"],
		resultSnapshotAnchorKey: report["anchorId"],
		"restore_result_id":     report["restoreResultId"],
		"capability_id":         summary["capability_id"],
	}, true, nil
}
