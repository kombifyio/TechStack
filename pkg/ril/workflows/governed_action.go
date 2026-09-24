package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
)

const (
	ActBeginGovernedAction    = "begin_governed_action"
	ActExecuteGovernedAction  = "execute_governed_action"
	ActCompleteGovernedAction = "complete_governed_action"
	ActFailGovernedAction     = "fail_governed_action"

	InputTenantID            = "tenant_id"
	InputOwnerSubjectID      = "owner_subject_id"
	InputCardID              = "card_id"
	InputExecutionID         = "execution_id"
	InputTraceID             = "trace_id"
	InputIdempotencyKey      = "idempotency_key"
	InputConnectorProjection = "connector_projection"

	fieldRequest         = "request"
	fieldAdmissionDigest = "admission_digest"
	fieldEvidence        = "evidence"
	fieldFailureReason   = "failure_reason"
	fieldErrorCode       = "error_code"
	fieldReplay          = "replay"
)

// ActionCardRemediationWorkflow adds durable checkpoint/resume semantics to
// the existing governed action authority and exact-once execution ledger. It
// deliberately starts from the explicit /execute command: approval remains a
// decision and never mutates a server by itself.
type ActionCardRemediationWorkflow struct{ policy workflow.RetryPolicy }

func NewActionCardRemediationWorkflow() *ActionCardRemediationWorkflow {
	return &ActionCardRemediationWorkflow{policy: workflow.DefaultRetryPolicy()}
}

func (w *ActionCardRemediationWorkflow) Type() workflow.RunType {
	return workflow.TypeActionCardRemediation
}

func (w *ActionCardRemediationWorkflow) RetryPolicy() workflow.RetryPolicy { return w.policy }

func (w *ActionCardRemediationWorkflow) Steps() []workflow.StepDef {
	return []workflow.StepDef{
		{Name: "admit", Run: w.admit, Compensate: w.fail},
		{Name: "execute", Run: w.execute},
		{Name: "verify", Run: w.verify},
		{Name: "complete", Run: w.complete},
	}
}

func (w *ActionCardRemediationWorkflow) admit(ctx context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
	if err := requireActionInputs(rc); err != nil {
		return workflow.StepResult{}, err
	}
	out, err := rc.Activities.Run(ctx, ActBeginGovernedAction, cloneMap(rc.Run.Input), idemKey(rc, "admit"))
	if err != nil {
		return workflow.StepResult{}, err
	}
	if reason := mapString(out, fieldFailureReason); reason != "" {
		setContext(rc, fieldFailureReason, reason)
		return workflow.StepResult{}, fmt.Errorf("governed action admission rejected: %s", reason)
	}
	if mapBool(out, fieldReplay) {
		setContext(rc, fieldReplay, true)
		return workflow.StepResult{Output: map[string]any{fieldReplay: true}}, nil
	}
	request, ok := out[fieldRequest].(map[string]any)
	if !ok || mapString(out, fieldAdmissionDigest) == "" {
		return workflow.StepResult{}, errors.New("governed action admission returned no bound request")
	}
	setContext(rc, fieldRequest, request)
	setContext(rc, fieldAdmissionDigest, mapString(out, fieldAdmissionDigest))
	return workflow.StepResult{Output: out}, nil
}

func (w *ActionCardRemediationWorkflow) execute(ctx context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
	if contextBool(rc, fieldReplay) {
		return workflow.StepResult{}, nil
	}
	request := contextMap(rc, fieldRequest)
	if request == nil {
		return workflow.StepResult{}, errors.New("governed action request checkpoint is missing")
	}
	out, err := rc.Activities.Run(ctx, ActExecuteGovernedAction, map[string]any{
		fieldRequest:         request,
		fieldAdmissionDigest: contextString(rc, fieldAdmissionDigest),
	}, idemKey(rc, "execute"))
	if err != nil {
		return workflow.StepResult{}, err
	}
	evidence, ok := out[fieldEvidence].(map[string]any)
	if !ok {
		return workflow.StepResult{}, errors.New("governed action execution returned no evidence")
	}
	setContext(rc, fieldEvidence, evidence)
	setContext(rc, fieldErrorCode, mapString(out, fieldErrorCode))
	return workflow.StepResult{Output: out}, nil
}

func (w *ActionCardRemediationWorkflow) verify(_ context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
	if contextBool(rc, fieldReplay) {
		return workflow.StepResult{}, nil
	}
	var request rilaction.Request
	var evidence rilaction.Evidence
	if err := decodeMap(contextMap(rc, fieldRequest), &request); err != nil {
		return workflow.StepResult{}, fmt.Errorf("decode governed action request checkpoint: %w", err)
	}
	if err := decodeMap(contextMap(rc, fieldEvidence), &evidence); err != nil {
		return workflow.StepResult{}, fmt.Errorf("decode governed action evidence checkpoint: %w", err)
	}
	if err := rilaction.ValidateEvidenceForRequest(request, evidence); err != nil {
		return workflow.StepResult{}, fmt.Errorf("verify governed action evidence: %w", err)
	}
	return workflow.StepResult{Output: map[string]any{"status": evidence.Status}}, nil
}

func (w *ActionCardRemediationWorkflow) complete(ctx context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
	if contextBool(rc, fieldReplay) {
		return workflow.StepResult{}, nil
	}
	return activityResult(rc.Activities.Run(ctx, ActCompleteGovernedAction, map[string]any{
		InputTenantID:    inputString(rc, InputTenantID),
		InputCardID:      inputString(rc, InputCardID),
		InputExecutionID: inputString(rc, InputExecutionID),
		fieldEvidence:    contextMap(rc, fieldEvidence),
		fieldErrorCode:   contextString(rc, fieldErrorCode),
	}, idemKey(rc, "complete")))
}

func (w *ActionCardRemediationWorkflow) fail(ctx context.Context, rc *workflow.RunContext) (workflow.StepResult, error) {
	if contextBool(rc, fieldReplay) {
		return workflow.StepResult{}, nil
	}
	errorCode := contextString(rc, fieldErrorCode)
	if errorCode == "" {
		errorCode = "workflow_execution_unavailable"
	}
	return activityResult(rc.Activities.Run(ctx, ActFailGovernedAction, map[string]any{
		InputTenantID:    inputString(rc, InputTenantID),
		InputCardID:      inputString(rc, InputCardID),
		InputExecutionID: inputString(rc, InputExecutionID),
		fieldErrorCode:   errorCode,
	}, idemKey(rc, "fail")))
}

func requireActionInputs(rc *workflow.RunContext) error {
	for _, key := range []string{InputTenantID, InputOwnerSubjectID, InputCardID, InputExecutionID, InputTraceID, InputIdempotencyKey} {
		if inputString(rc, key) == "" {
			return fmt.Errorf("action_card_remediation: %s is required", key)
		}
	}
	return nil
}

func activityResult(out map[string]any, err error) (workflow.StepResult, error) {
	return workflow.StepResult{Output: out}, err
}

func contextMap(rc *workflow.RunContext, key string) map[string]any {
	if rc == nil || rc.Run == nil || rc.Run.Context == nil {
		return nil
	}
	value, _ := rc.Run.Context[key].(map[string]any)
	return value
}

func contextBool(rc *workflow.RunContext, key string) bool {
	if rc == nil || rc.Run == nil || rc.Run.Context == nil {
		return false
	}
	value, _ := rc.Run.Context[key].(bool)
	return value
}

func cloneMap(source map[string]any) map[string]any {
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func decodeMap(source map[string]any, destination any) error {
	data, err := json.Marshal(source)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, destination)
}
