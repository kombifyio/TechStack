package workflows

import (
	"context"
	"errors"
	"fmt"

	"github.com/kombifyio/techstack/pkg/ril/workflow"
)

const (
	ActQueryMonitoringSignal        = "query_monitoring_signal"
	ActDispatchGovernedOperation    = "dispatch_governed_operation"
	ActVerifyGovernedOperation      = "verify_governed_operation"
	ActExecuteGovernedCompensation  = "execute_governed_compensation"
	ActRecordGovernedOperationAudit = "record_governed_operation_audit"

	InputAuthorizationRef = "authorization_ref"
	InputOperationRef     = "operation_ref"
	InputCompensationRef  = "compensation_ref"
	InputTargets          = "targets"

	fieldMonitoringSignal = "monitoring_signal"
)

// The three closed operational definitions intentionally describe orchestration
// only. Product adapters provide the named activities; the definitions do not
// create another provider, transport, certificate, or inventory authority.
type DriftCorrectionWorkflow struct{ governedOperationWorkflow }
type CertRotationWorkflow struct{ governedOperationWorkflow }
type RollingUpdateWorkflow struct{ governedOperationWorkflow }

func NewDriftCorrectionWorkflow() *DriftCorrectionWorkflow {
	return &DriftCorrectionWorkflow{governedOperationWorkflow{typ: workflow.TypeDriftCorrection, operation: "drift-correction", observe: true}}
}

func NewCertRotationWorkflow() *CertRotationWorkflow {
	return &CertRotationWorkflow{governedOperationWorkflow{typ: workflow.TypeCertRotation, operation: "certificate-rotation"}}
}

func NewRollingUpdateWorkflow() *RollingUpdateWorkflow {
	return &RollingUpdateWorkflow{governedOperationWorkflow{typ: workflow.TypeRollingUpdate, operation: "rolling-update"}}
}

type governedOperationWorkflow struct {
	typ       workflow.RunType
	operation string
	observe   bool
}

func (w *governedOperationWorkflow) Type() workflow.RunType { return w.typ }
func (w *governedOperationWorkflow) RetryPolicy() workflow.RetryPolicy {
	return workflow.DefaultRetryPolicy()
}

func (w *governedOperationWorkflow) Steps() []workflow.StepDef {
	steps := make([]workflow.StepDef, 0, 4)
	if w.observe {
		steps = append(steps, workflow.StepDef{Name: "observe", Run: w.observeSignal})
	}
	return append(steps,
		workflow.StepDef{Name: "dispatch", Run: w.dispatch, Compensate: w.compensate},
		workflow.StepDef{Name: "verify", Run: w.verify},
		workflow.StepDef{Name: "audit", Run: w.audit},
	)
}

func (w *governedOperationWorkflow) observeSignal(ctx context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
	out, err := rc.Activities.Run(ctx, ActQueryMonitoringSignal, w.activityInput(rc), idemKey(rc, "observe"))
	if err != nil {
		return workflow.StepResult{}, err
	}
	if active, ok := out["active"].(bool); !ok || !active {
		return workflow.StepResult{}, errors.New("drift correction requires an active monitoring signal")
	}
	setContext(rc, fieldMonitoringSignal, out)
	return workflow.StepResult{Output: out}, nil
}

func (w *governedOperationWorkflow) dispatch(ctx context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
	if err := w.validateInput(rc); err != nil {
		return workflow.StepResult{}, err
	}
	out, err := rc.Activities.Run(ctx, ActDispatchGovernedOperation, w.activityInput(rc), idemKey(rc, "dispatch"))
	if err != nil {
		return workflow.StepResult{}, err
	}
	setContext(rc, "operation_result", out)
	return workflow.StepResult{Output: out}, nil
}

func (w *governedOperationWorkflow) verify(ctx context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
	input := w.activityInput(rc)
	input["operation_result"] = contextMap(rc, "operation_result")
	out, err := rc.Activities.Run(ctx, ActVerifyGovernedOperation, input, idemKey(rc, "verify"))
	if err != nil {
		return workflow.StepResult{}, err
	}
	if verified, ok := out["verified"].(bool); !ok || !verified {
		return workflow.StepResult{}, fmt.Errorf("%s verification failed", w.operation)
	}
	return workflow.StepResult{Output: out}, nil
}

func (w *governedOperationWorkflow) audit(ctx context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
	input := w.activityInput(rc)
	input["outcome"] = "completed"
	return activityResult(rc.Activities.Run(ctx, ActRecordGovernedOperationAudit, input, idemKey(rc, "audit")))
}

func (w *governedOperationWorkflow) compensate(ctx context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
	input := w.activityInput(rc)
	input["operation_result"] = contextMap(rc, "operation_result")
	return activityResult(rc.Activities.Run(ctx, ActExecuteGovernedCompensation, input, idemKey(rc, "compensate")))
}

func (w *governedOperationWorkflow) validateInput(rc *workflow.RunContext) error {
	if inputString(rc, InputAuthorizationRef) == "" || inputString(rc, InputOperationRef) == "" || inputString(rc, InputCompensationRef) == "" {
		return fmt.Errorf("%s requires bounded authorization, operation, and compensation references", w.operation)
	}
	if w.typ == workflow.TypeRollingUpdate {
		if targetCount(rc.Run.Input[InputTargets]) == 0 {
			return errors.New("rolling update requires at least one target")
		}
	}
	return nil
}

func targetCount(value any) int {
	switch targets := value.(type) {
	case []any:
		return len(targets)
	case []string:
		return len(targets)
	default:
		return 0
	}
}

func (w *governedOperationWorkflow) activityInput(rc *workflow.RunContext) map[string]any {
	out := cloneMap(rc.Run.Input)
	out["operation"] = w.operation
	if signal := contextMap(rc, fieldMonitoringSignal); signal != nil {
		out[fieldMonitoringSignal] = signal
	}
	return out
}

var (
	_ workflow.WorkflowDefinition = (*DriftCorrectionWorkflow)(nil)
	_ workflow.WorkflowDefinition = (*CertRotationWorkflow)(nil)
	_ workflow.WorkflowDefinition = (*RollingUpdateWorkflow)(nil)
)
