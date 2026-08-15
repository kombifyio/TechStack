// Package workflows holds concrete RIL WorkflowDefinitions executed by the
// pkg/ril/workflow engine. Definitions are declarative, side-effect-free step
// lists: every external effect goes through a named, idempotency-keyed activity
// (pkg/ril/activities, wired at startup), so the definitions stay unit-testable
// with fake activities and the engine can checkpoint/resume by step index.
package workflows

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kombifyio/techstack/pkg/ril/workflow"
)

// Activity names dispatched by the service-migration workflow. Concrete
// implementations are registered on the workflow.ActivityRunner at startup.
const (
	ActMarkServiceStatus     = "mark_service_status"
	ActCreateTargetService   = "create_target_service"
	ActDeleteService         = "delete_service"
	ActDeployServiceToNode   = "deploy_service_to_node"
	ActRemoveServiceFromNode = "remove_service_from_node"
	ActVerifyServiceHealthy  = "verify_service_healthy"
	ActSwitchServiceTraffic  = "switch_service_traffic"
	ActDrainServiceOnNode    = "drain_service_on_node"
)

// Run.Input keys the workflow reads, and the context key it threads forward.
const (
	InputSourceServiceID = "source_service_id"
	InputTargetServerID  = "target_server_id"
	InputStackID         = "stack_id"
	InputServiceName     = "service_name"

	ctxTargetServiceID = "target_service_id"

	// fieldServiceID is the common activity-input key for the service id.
	fieldServiceID = "service_id"
)

// MigrationConfirmSignalPrefix + run id is the signal the confirm step awaits.
// The verify endpoint (manual override) or an auto-confirm-on-healthy hook
// delivers it via engine.Deliver.
const MigrationConfirmSignalPrefix = "migration_confirm:"

// MigrationConfirmSignalKey returns the confirm signal key for a run.
func MigrationConfirmSignalKey(runID string) string {
	return MigrationConfirmSignalPrefix + runID
}

// ServiceMigrationWorkflow relocates a managed service from its current node to a
// target node: claim -> provision-target -> verify-target -> confirm -> cutover
// -> drain-source -> archive-source, with saga compensation that restores the
// source and removes the half-migrated target on failure. It is the durable
// replacement for the fake createServiceMigrationJob.
// See docs/plans/2026-06-04-service-migration-execution.md.
type ServiceMigrationWorkflow struct {
	policy         workflow.RetryPolicy
	confirmTimeout time.Duration
}

// NewServiceMigrationWorkflow builds the workflow with sensible defaults.
func NewServiceMigrationWorkflow() *ServiceMigrationWorkflow {
	return &ServiceMigrationWorkflow{
		policy:         workflow.DefaultRetryPolicy(),
		confirmTimeout: time.Hour,
	}
}

func (w *ServiceMigrationWorkflow) Type() workflow.RunType { return workflow.TypeServiceMigration }

func (w *ServiceMigrationWorkflow) RetryPolicy() workflow.RetryPolicy { return w.policy }

func (w *ServiceMigrationWorkflow) Steps() []workflow.StepDef {
	return []workflow.StepDef{
		{Name: "claim", Run: w.claim, Compensate: w.compensateClaim},
		{Name: "provision-target", Run: w.provisionTarget, Compensate: w.compensateProvision},
		{Name: "verify-target", Run: w.verifyTarget},
		{Name: "confirm", Run: w.confirm},
		{Name: "cutover", Run: w.cutover, Compensate: w.compensateCutover},
		{Name: "drain-source", Run: w.drainSource},
		{Name: "archive-source", Run: w.archiveSource},
	}
}

// claim marks the source service as migrating and creates the target service
// record (status deploying). The target id is threaded forward via run context.
func (w *ServiceMigrationWorkflow) claim(ctx context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
	source := inputString(rc, InputSourceServiceID)
	target := inputString(rc, InputTargetServerID)
	stack := inputString(rc, InputStackID)
	if source == "" || target == "" {
		return workflow.StepResult{}, errors.New("service_migration: source_service_id and target_server_id are required")
	}
	if _, err := rc.Activities.Run(ctx, ActMarkServiceStatus, map[string]any{
		fieldServiceID: source,
		"status":       "migrating",
	}, idemKey(rc, "claim:mark-migrating")); err != nil {
		return workflow.StepResult{}, err
	}
	out, err := rc.Activities.Run(ctx, ActCreateTargetService, map[string]any{
		"source_service_id": source,
		"target_server_id":  target,
		"stack_id":          stack,
	}, idemKey(rc, "claim:create-target"))
	if err != nil {
		return workflow.StepResult{}, err
	}
	targetServiceID := mapString(out, ctxTargetServiceID)
	if targetServiceID == "" {
		return workflow.StepResult{}, errors.New("service_migration: create_target_service did not return target_service_id")
	}
	setContext(rc, ctxTargetServiceID, targetServiceID)
	return workflow.StepResult{Output: map[string]any{ctxTargetServiceID: targetServiceID}}, nil
}

// compensateClaim removes the created target record and restores the source.
func (w *ServiceMigrationWorkflow) compensateClaim(ctx context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
	if target := contextString(rc, ctxTargetServiceID); target != "" {
		if _, err := rc.Activities.Run(ctx, ActDeleteService, map[string]any{
			fieldServiceID: target,
		}, idemKey(rc, "claim:compensate-delete")); err != nil {
			return workflow.StepResult{}, err
		}
	}
	if source := inputString(rc, InputSourceServiceID); source != "" {
		if _, err := rc.Activities.Run(ctx, ActMarkServiceStatus, map[string]any{
			fieldServiceID: source,
			"status":       "running",
		}, idemKey(rc, "claim:compensate-restore")); err != nil {
			return workflow.StepResult{}, err
		}
	}
	return workflow.StepResult{}, nil
}

// provisionTarget deploys the workload onto the target node.
func (w *ServiceMigrationWorkflow) provisionTarget(ctx context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
	target := contextString(rc, ctxTargetServiceID)
	if target == "" {
		return workflow.StepResult{}, errors.New("service_migration: missing target_service_id in run context")
	}
	if _, err := rc.Activities.Run(ctx, ActDeployServiceToNode, map[string]any{
		ctxTargetServiceID: target,
		"target_server_id": inputString(rc, InputTargetServerID),
		"stack_id":         inputString(rc, InputStackID),
	}, idemKey(rc, "provision-target")); err != nil {
		return workflow.StepResult{}, err
	}
	return workflow.StepResult{}, nil
}

// compensateProvision removes the deployed workload from the target node.
func (w *ServiceMigrationWorkflow) compensateProvision(ctx context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
	target := contextString(rc, ctxTargetServiceID)
	if target == "" {
		return workflow.StepResult{}, nil
	}
	if _, err := rc.Activities.Run(ctx, ActRemoveServiceFromNode, map[string]any{
		ctxTargetServiceID: target,
		"target_server_id": inputString(rc, InputTargetServerID),
	}, idemKey(rc, "provision-target:compensate")); err != nil {
		return workflow.StepResult{}, err
	}
	return workflow.StepResult{}, nil
}

// verifyTarget polls the target service health; an unhealthy result errors so the
// engine retries and, once exhausted, rolls the migration back.
func (w *ServiceMigrationWorkflow) verifyTarget(ctx context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
	target := contextString(rc, ctxTargetServiceID)
	out, err := rc.Activities.Run(ctx, ActVerifyServiceHealthy, map[string]any{
		ctxTargetServiceID: target,
	}, idemKey(rc, "verify-target"))
	if err != nil {
		return workflow.StepResult{}, err
	}
	if !mapBool(out, "healthy") {
		return workflow.StepResult{}, fmt.Errorf("service_migration: target service %q not healthy", target)
	}
	return workflow.StepResult{}, nil
}

// confirm suspends for operator confirmation (auto-confirm-on-healthy or manual
// override delivers the signal). A timeout or explicit rejection aborts the run,
// triggering rollback of the completed steps.
func (w *ServiceMigrationWorkflow) confirm(_ context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
	if rc.Signal == nil {
		return workflow.StepResult{Suspend: &workflow.SuspendDirective{
			SignalKey: MigrationConfirmSignalKey(rc.Run.RunID),
			Timeout:   w.confirmTimeout,
			TimerKind: workflow.TimerEscalation,
		}}, nil
	}
	if rc.Signal.TimedOut {
		return workflow.StepResult{}, errors.New("service_migration: confirmation timed out")
	}
	if confirmed, ok := rc.Signal.Payload["confirmed"].(bool); ok && !confirmed {
		return workflow.StepResult{}, errors.New("service_migration: migration rejected by operator")
	}
	return workflow.StepResult{Output: map[string]any{"confirmed": true}}, nil
}

// cutover switches traffic to the target and marks it running.
func (w *ServiceMigrationWorkflow) cutover(ctx context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
	source := inputString(rc, InputSourceServiceID)
	target := contextString(rc, ctxTargetServiceID)
	if _, err := rc.Activities.Run(ctx, ActSwitchServiceTraffic, map[string]any{
		"from_service_id": source,
		"to_service_id":   target,
	}, idemKey(rc, "cutover:switch")); err != nil {
		return workflow.StepResult{}, err
	}
	if _, err := rc.Activities.Run(ctx, ActMarkServiceStatus, map[string]any{
		fieldServiceID: target,
		"status":       "running",
	}, idemKey(rc, "cutover:mark-running")); err != nil {
		return workflow.StepResult{}, err
	}
	return workflow.StepResult{}, nil
}

// compensateCutover reverts traffic back to the source.
func (w *ServiceMigrationWorkflow) compensateCutover(ctx context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
	source := inputString(rc, InputSourceServiceID)
	target := contextString(rc, ctxTargetServiceID)
	if _, err := rc.Activities.Run(ctx, ActSwitchServiceTraffic, map[string]any{
		"from_service_id": target,
		"to_service_id":   source,
	}, idemKey(rc, "cutover:compensate-revert")); err != nil {
		return workflow.StepResult{}, err
	}
	return workflow.StepResult{}, nil
}

// drainSource stops/drains the workload on the source node after cutover.
func (w *ServiceMigrationWorkflow) drainSource(ctx context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
	source := inputString(rc, InputSourceServiceID)
	if _, err := rc.Activities.Run(ctx, ActDrainServiceOnNode, map[string]any{
		fieldServiceID: source,
	}, idemKey(rc, "drain-source")); err != nil {
		return workflow.StepResult{}, err
	}
	return workflow.StepResult{}, nil
}

// archiveSource marks the now-drained source service as archived.
func (w *ServiceMigrationWorkflow) archiveSource(ctx context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
	source := inputString(rc, InputSourceServiceID)
	if _, err := rc.Activities.Run(ctx, ActMarkServiceStatus, map[string]any{
		fieldServiceID: source,
		"status":       "archived",
	}, idemKey(rc, "archive-source")); err != nil {
		return workflow.StepResult{}, err
	}
	return workflow.StepResult{}, nil
}

// idemKey derives a stable idempotency key per (run, step-action) so retries and
// restarts dedup the underlying side effect.
func idemKey(rc *workflow.RunContext, action string) string {
	return rc.Run.RunID + ":" + action
}

func inputString(rc *workflow.RunContext, key string) string {
	if rc == nil || rc.Run == nil {
		return ""
	}
	return mapString(rc.Run.Input, key)
}

func contextString(rc *workflow.RunContext, key string) string {
	if rc == nil || rc.Run == nil {
		return ""
	}
	return mapString(rc.Run.Context, key)
}

func setContext(rc *workflow.RunContext, key string, value any) {
	if rc.Run.Context == nil {
		rc.Run.Context = map[string]any{}
	}
	rc.Run.Context[key] = value
}

func mapString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func mapBool(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	v, ok := m[key].(bool)
	return ok && v
}
