//nolint:goconst // moved unchanged from deploy_handler.go; repeated field names predate the split
package jobs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/secrets"
)

func (r *deployRollout) bootstrapManagedRuntimeTarget(ctx context.Context) error {
	if !r.managedRuntime || r.cfg == nil {
		return nil
	}
	target := normalizeRuntimeActionTarget(r.actionReq.RuntimeTarget)
	if !runtimeActionTargetHasSSHCredential(target) {
		return nil
	}
	started := time.Now()
	r.q.UpdateProgress(r.job.ID, 72, "Preparing managed VPS with StackKits CLI...")
	result, err := r.runStackKitPrepare(ctx, target)
	if err != nil {
		reason := runtimeTargetBootstrapReason(result, err)
		r.recordTargetBootstrapProof(result, reason, "failed", err)
		bundle := r.collectRuntimeDiagnostics(ctx, nil, r.actionReq, "target_bootstrap", reason, time.Since(started), err)
		if bundle != nil {
			r.q.addLog(r.job, "warn", fmt.Sprintf("Managed VPS bootstrap diagnostics %s for %s", bundle.Status, reason))
		}
		return err
	}
	if result == nil {
		return nil
	}
	r.recordTargetBootstrapProof(result, runtimeTargetBootstrapReason(result, nil), "ready", nil)
	r.e2eProof["target_bootstrap"] = result.Status
	r.q.addLog(r.job, "info", fmt.Sprintf("Managed VPS bootstrap %s: %s", firstNonEmpty(result.Status, "completed"), result.Message))
	return nil
}

func (r *deployRollout) runStackKitPrepare(ctx context.Context, target *RuntimeActionTarget) (*RuntimeTargetBootstrapResult, error) {
	prepRunner := r.cfg.RuntimeActions.StackKitPrepRunner
	if prepRunner == nil &&
		strings.TrimSpace(r.actionReq.StackSpecPath) != "" &&
		!truthyRuntimeActionEnv("TECHSTACK_STACKKIT_PREP_DISABLED") {
		release, err := configuredPinnedStackKitRelease()
		if err != nil {
			return nil, err
		}
		// Only the pinned published release prepares a target; there is no
		// source-built StackKits CLI fallback.
		if release != nil {
			prepRunner = NewPinnedStackKitCLIPrepRunner(*release)
		}
	}
	prepRunner = r.stackKitPrepRunnerWithProgress(prepRunner)
	if prepRunner != nil {
		preBootstrap, bootstrapErr := r.preBootstrapManagedRuntimeTarget(ctx, target)
		if bootstrapErr != nil {
			return preBootstrap, bootstrapErr
		}
		r.updateJobStepProgress(StepStackKitPrepare, 72, "Preparing managed VPS with StackKits CLI...")
		result, err := prepRunner.PrepareStackKitRuntimeTarget(ctx, r.actionReq)
		if result != nil || err != nil {
			return preferExecutedBootstrapProof(preBootstrap, result), err
		}
	}
	if r.cfg.RuntimeActions.TargetBootstrapper == nil {
		return nil, nil
	}
	r.updateJobStepProgress(StepStackKitPrepare, 72, "Preparing managed VPS with SSH bootstrap fallback...")
	return r.cfg.RuntimeActions.TargetBootstrapper.BootstrapRuntimeTarget(ctx, target)
}

func (r *deployRollout) preBootstrapManagedRuntimeTarget(ctx context.Context, target *RuntimeActionTarget) (*RuntimeTargetBootstrapResult, error) {
	if r == nil || r.cfg == nil || r.cfg.RuntimeActions.TargetBootstrapper == nil ||
		truthyRuntimeActionEnv("TECHSTACK_STACKKIT_PREP_PREBOOTSTRAP_DISABLED") {
		return nil, nil
	}
	r.updateJobStepProgress(StepDockerReady, 74, "Preparing managed VPS Docker runtime before StackKits...")
	var result *RuntimeTargetBootstrapResult
	var err error
	if bootstrapper, ok := r.cfg.RuntimeActions.TargetBootstrapper.(interface {
		BootstrapRuntimeTargetWithProgress(context.Context, *RuntimeActionTarget, func(stackKitCLIProgressEvent)) (*RuntimeTargetBootstrapResult, error)
	}); ok {
		result, err = bootstrapper.BootstrapRuntimeTargetWithProgress(ctx, target, r.handleStackKitPrepProgress)
	} else {
		result, err = r.cfg.RuntimeActions.TargetBootstrapper.BootstrapRuntimeTarget(ctx, target)
	}
	if err != nil && r.cfg.StackKitCommander != nil &&
		classifyRuntimeTargetBootstrapError(err, "") == RuntimeTargetBootstrapSSHAuth {
		// Host-security disables ubuntu/root. Guard HTTPS remains the Apply
		// channel. A live Auth0 Centron retry failed here while Coolify was
		// already running and the agent was connected.
		if r.q != nil && r.job != nil {
			r.q.addLog(r.job, "warn", "SSH bootstrap unavailable after host-security; continuing typed Apply over Guard")
		}
		return &RuntimeTargetBootstrapResult{
			Status:  RuntimeTargetBootstrapReady,
			Message: "SSH bootstrap skipped because Guard is connected",
		}, nil
	}
	if r.q != nil && r.job != nil && result != nil {
		r.q.addLog(r.job, "info", fmt.Sprintf("Managed VPS pre-bootstrap %s: %s", firstNonEmpty(result.Status, "completed"), result.Message))
	}
	return result, err
}

func (r *deployRollout) stackKitPrepRunnerWithProgress(prepRunner StackKitPrepRunner) StackKitPrepRunner {
	cliRunner, ok := prepRunner.(*StackKitCLIPrepRunner)
	if !ok || cliRunner == nil {
		return prepRunner
	}
	clone := *cliRunner
	clone.Progress = r.handleStackKitPrepProgress
	return &clone
}

func (r *deployRollout) handleStackKitPrepProgress(event stackKitCLIProgressEvent) {
	step, progress := stackKitPrepProgressStep(event)
	r.updateJobStepProgress(step, progress, stackKitPrepProgressMessage(event))
}

func (r *deployRollout) updateJobStepProgress(step string, progress int, message string) {
	if r == nil || r.job == nil {
		return
	}
	currentStep, currentProgress := r.job.stepProgress()
	if rolloutStepBefore(step, currentStep) {
		// Prepare progress events may name an earlier task (the CLI connects to
		// the target again, Docker pre-bootstrap precedes the CLI prepare).
		// The reported position only moves forward; the message still lands.
		step, progress = currentStep, currentProgress
	}
	if strings.TrimSpace(step) != "" {
		r.job.setStep(step)
	}
	if r.q != nil {
		r.q.UpdateProgress(r.job.ID, progress, message)
	}
}

// rolloutTaskOrder is the creation page's rollout task order.
var rolloutTaskOrder = []string{
	StepCreateLease, StepPrepareRollout, StepRuntimeConnected, StepTelemetryHandshake,
	StepValidateWorkers, StepGenerateUnified, StepPersistUnified, StepGenerateIaC,
	StepSimulationGate, StepStackKitPrepare, StepDockerReady, StepOpenTofuReady,
	StepTerramateReady, StepTelemetryReady, StepRolloutRunner, StepServiceInventory,
	StepVerifyRollout, StepRestoreDrill,
}

// rolloutStepBefore reports whether next is an earlier rollout task than
// current. Steps outside the task order never count as earlier.
func rolloutStepBefore(next, current string) bool {
	nextIndex, currentIndex := -1, -1
	for index, step := range rolloutTaskOrder {
		if step == next {
			nextIndex = index
		}
		if step == current {
			currentIndex = index
		}
	}
	return nextIndex >= 0 && currentIndex >= 0 && nextIndex < currentIndex
}

func stackKitPrepProgressStep(event stackKitCLIProgressEvent) (string, int) {
	phase := strings.ToLower(strings.TrimSpace(event.Phase))
	switch {
	case strings.Contains(phase, "docker"), phase == "apt_wait":
		return StepDockerReady, 74
	case strings.Contains(phase, "opentofu"), strings.Contains(phase, "tofu"):
		return StepOpenTofuReady, 76
	case strings.Contains(phase, "terramate"):
		return StepTerramateReady, 78
	case strings.Contains(phase, "telemetry"), strings.Contains(phase, "otel"), strings.Contains(phase, "metrics"):
		return StepTelemetryReady, 80
	case strings.Contains(phase, "target.connect"), strings.Contains(phase, "target.inspect"), strings.Contains(phase, "ssh"):
		return StepRuntimeConnected, 73
	default:
		return StepStackKitPrepare, 72
	}
}

func stackKitPrepProgressMessage(event stackKitCLIProgressEvent) string {
	message := strings.TrimSpace(event.Message)
	if message != "" {
		return message
	}
	phase := strings.TrimSpace(event.Phase)
	if phase == "" {
		return "Preparing managed VPS with StackKits CLI..."
	}
	status := strings.TrimSpace(event.Status)
	if status == "" {
		return fmt.Sprintf("StackKits prepare: %s", phase)
	}
	return fmt.Sprintf("StackKits prepare: %s %s", phase, status)
}

func runtimeTargetBootstrapReason(result *RuntimeTargetBootstrapResult, err error) string {
	if result != nil && strings.TrimSpace(result.ReasonCode) != "" {
		return strings.TrimSpace(result.ReasonCode)
	}
	return classifyRuntimeTargetBootstrapError(err, "")
}

func (r *deployRollout) recordTargetBootstrapProof(result *RuntimeTargetBootstrapResult, reason, fallbackStatus string, err error) {
	status := fallbackStatus
	message := ""
	var durationMS int64
	var attempts int
	output := ""
	if result != nil {
		status = firstNonEmpty(result.Status, status)
		message = result.Message
		durationMS = result.DurationMS
		attempts = result.Attempts
		output = result.Output
	}
	if message == "" && err != nil {
		message = err.Error()
	}
	if reason == "" {
		reason = RuntimeTargetBootstrapUnknown
	}
	proof := map[string]interface{}{
		"status":      status,
		"reason_code": reason,
		"message":     secrets.Redact(message),
		"duration_ms": durationMS,
	}
	if attempts > 0 {
		proof["attempts"] = attempts
	}
	if output != "" {
		proof["output_snippet"] = truncateRuntimeDiagnosticsOutput(secrets.Redact(output), 4096)
	}
	if result != nil && len(result.Events) > 0 {
		proof["events"] = sanitizeStackKitPrepEvents(result.Events)
	}
	if r.runtimeProof == nil {
		r.runtimeProof = map[string]interface{}{}
	}
	if r.e2eProof == nil {
		r.e2eProof = map[string]any{}
	}
	r.runtimeProof["target_bootstrap"] = proof
	r.e2eProof["target_bootstrap"] = status
	r.e2eProof["target_bootstrap_reason"] = reason

	r.job.mutateResult(func(result map[string]interface{}) {
		result["target_bootstrap"] = proof
	})
}

func sanitizeStackKitPrepEvents(events []stackKitCLIProgressEvent) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(events))
	for _, event := range events {
		entry := map[string]interface{}{
			"phase":  strings.TrimSpace(event.Phase),
			"status": strings.TrimSpace(event.Status),
		}
		if !event.Time.IsZero() {
			entry["time"] = event.Time.Format(time.RFC3339Nano)
		}
		if message := strings.TrimSpace(event.Message); message != "" {
			entry["message"] = secrets.Redact(message)
		}
		if failureClass := strings.TrimSpace(event.FailureClass); failureClass != "" {
			entry["failure_class"] = failureClass
		}
		if len(event.Attributes) > 0 {
			attrs := make(map[string]string, len(event.Attributes))
			for key, value := range event.Attributes {
				attrs[key] = secrets.Redact(value)
			}
			entry["attributes"] = attrs
		}
		out = append(out, entry)
	}
	return out
}

func (r *deployRollout) collectRuntimeDiagnostics(ctx context.Context, runner RuntimeActionRunner, req RuntimeActionRequest, action, reason string, elapsed time.Duration, runtimeErr error) *RuntimeDiagnosticsBundle {
	collector := r.cfg.RuntimeActions.DiagnosticsCollector
	if collector == nil {
		return nil
	}
	diagnosticCtx, cancel := context.WithTimeout(ctx, defaultRuntimeDiagnosticsTimeout+5*time.Second)
	defer cancel()

	snapshot := r.job.Snapshot()
	leaseID := firstNonEmpty(stringFromMap(snapshot.Result, leaseIDField), stringFromMap(snapshot.Payload, leaseIDField))
	serverID := runtimeidentity.LeaseServerID(leaseID)
	runtimeAgentID := runtimeidentity.LeaseRuntimeAgentID(req.TenantID, leaseID)
	if req.TechStackEnrollment != nil {
		serverID = firstNonEmpty(req.TechStackEnrollment.ServerID, serverID)
		runtimeAgentID = firstNonEmpty(req.TechStackEnrollment.RuntimeAgentID, runtimeAgentID)
	}
	request := RuntimeDiagnosticsRequest{
		Action:         action,
		Reason:         reason,
		JobID:          r.job.ID,
		StackID:        r.job.TargetID,
		TenantID:       strings.TrimSpace(req.TenantID),
		LeaseID:        leaseID,
		OperationID:    firstNonEmpty(stringFromMap(snapshot.Result, "operation_id"), stringFromMap(snapshot.Payload, "operation_id")),
		ServerID:       serverID,
		RuntimeAgentID: runtimeAgentID,
		Provider:       firstNonEmpty(stringFromMap(snapshot.Result, providerField), stringFromMap(snapshot.Payload, providerField)),
		TargetKind:     r.targetKind,
		RuntimeTarget:  req.RuntimeTarget,
		ActionEndpoint: runtimeActionDescriptor(runner),
		Elapsed:        elapsed,
		Err:            runtimeErr,
	}
	bundle, err := collector.CollectRuntimeDiagnostics(diagnosticCtx, request)
	if err != nil {
		r.q.addLog(r.job, "warn", fmt.Sprintf("Runtime diagnostics collection failed: %s", secrets.Redact(err.Error())))
		captureJobError(ctx, err, map[string]interface{}{
			stepField:    action,
			stackIDField: r.job.TargetID,
			reasonField:  reason,
		})
		return nil
	}
	if bundle == nil {
		return nil
	}
	r.recordRuntimeDiagnostics(bundle, elapsed)
	breadcrumbStep(ctx, action, "runtime diagnostics collected", map[string]interface{}{
		"status":     bundle.Status,
		reasonField:  bundle.Reason,
		"elapsed_ms": elapsed.Milliseconds(),
	})
	return bundle
}

func (r *deployRollout) recordRuntimeDiagnostics(bundle *RuntimeDiagnosticsBundle, elapsed time.Duration) {
	if bundle == nil {
		return
	}
	entry := runtimeDiagnosticsBundleMap(bundle)
	if entry == nil {
		return
	}
	entry["job_id"] = r.job.ID
	entry[stackIDField] = r.job.TargetID
	entry["elapsed_before_collection_ms"] = elapsed.Milliseconds()

	r.job.mutateResult(func(result map[string]interface{}) {
		result["runtime_diagnostics"] = entry
	})

	switch bundle.Status {
	case "collected":
		r.q.addLog(r.job, "info", fmt.Sprintf("Runtime diagnostics collected for %s (%d commands)", bundle.Action, len(bundle.Commands)))
	case "skipped":
		r.q.addLog(r.job, "warn", fmt.Sprintf("Runtime diagnostics skipped for %s: %s", bundle.Action, bundle.Error))
	default:
		r.q.addLog(r.job, "warn", fmt.Sprintf("Runtime diagnostics %s for %s: %s", bundle.Status, bundle.Action, bundle.Error))
	}
}

func runtimeActionDescriptor(runner RuntimeActionRunner) RuntimeActionDescriptor {
	if describer, ok := runner.(RuntimeActionDescriber); ok {
		return describer.RuntimeActionDescriptor()
	}
	return RuntimeActionDescriptor{}
}
