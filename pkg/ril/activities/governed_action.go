// Package activities binds the closed RIL workflow definitions to existing
// Techstack authorities. It contains adapters, not a second action-card,
// transport, provider, or execution authority.
package activities

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
	"github.com/kombifyio/techstack/pkg/ril/actions"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
	"github.com/kombifyio/techstack/pkg/ril/workflows"
)

type GovernedActionExecutor interface {
	Execute(context.Context, rilaction.Request, string) (rilaction.Evidence, error)
}

type GovernedActionActivities struct {
	Authority actions.Authority
	Executor  GovernedActionExecutor
	Now       func() time.Time
}

type ActivityRegistrar interface {
	RegisterActivity(string, workflow.ActivityFunc)
}

func (a GovernedActionActivities) Register(registrar ActivityRegistrar) error {
	if registrar == nil || a.Authority == nil || a.Executor == nil {
		return errors.New("ril activities: runner, authority, and executor are required")
	}
	if a.Now == nil {
		a.Now = func() time.Time { return time.Now().UTC() }
	}
	registrar.RegisterActivity(workflows.ActBeginGovernedAction, a.begin)
	registrar.RegisterActivity(workflows.ActExecuteGovernedAction, a.execute)
	registrar.RegisterActivity(workflows.ActCompleteGovernedAction, a.complete)
	registrar.RegisterActivity(workflows.ActFailGovernedAction, a.fail)
	return nil
}

func (a GovernedActionActivities) begin(ctx context.Context, input map[string]any) (map[string]any, error) {
	projection, err := optionalProjection(input[workflows.InputConnectorProjection])
	if err != nil {
		return nil, err
	}
	result, err := a.Authority.Begin(ctx, actions.BeginExecution{
		TenantID:            stringValue(input, workflows.InputTenantID),
		OwnerSubjectID:      stringValue(input, workflows.InputOwnerSubjectID),
		CardID:              stringValue(input, workflows.InputCardID),
		ExecutionID:         stringValue(input, workflows.InputExecutionID),
		TraceID:             stringValue(input, workflows.InputTraceID),
		IdempotencyKey:      stringValue(input, workflows.InputIdempotencyKey),
		Now:                 a.Now(),
		ConnectorProjection: projection,
	})
	if err != nil {
		if reason := actionFailureReason(err); reason != "" {
			return map[string]any{"failure_reason": reason}, nil
		}
		return nil, err
	}
	if result.Disposition == actions.BeginReplay {
		return map[string]any{"replay": true}, nil
	}
	request, err := objectMap(result.Request)
	if err != nil {
		return nil, err
	}
	return map[string]any{"request": request, "admission_digest": result.Admission.Digest}, nil
}

func (a GovernedActionActivities) execute(ctx context.Context, input map[string]any) (map[string]any, error) {
	var request rilaction.Request
	if err := decodeObject(input["request"], &request); err != nil {
		return nil, fmt.Errorf("ril activities: decode governed request: %w", err)
	}
	evidence, executionErr := a.Executor.Execute(ctx, request, stringValue(input, "admission_digest"))
	if evidence.ExecutionID == "" {
		if executionErr != nil {
			return nil, executionErr
		}
		return nil, errors.New("ril activities: executor returned no evidence")
	}
	encoded, err := objectMap(evidence)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"evidence": encoded}
	if executionErr != nil {
		out["error_code"] = "stackkit_execution_failed"
	}
	return out, nil
}

func (a GovernedActionActivities) complete(ctx context.Context, input map[string]any) (map[string]any, error) {
	var evidence rilaction.Evidence
	if err := decodeObject(input["evidence"], &evidence); err != nil {
		return nil, fmt.Errorf("ril activities: decode governed evidence: %w", err)
	}
	_, err := a.Authority.Complete(ctx,
		stringValue(input, workflows.InputTenantID),
		stringValue(input, workflows.InputCardID),
		evidence,
		stringValue(input, "error_code"),
		a.Now(),
	)
	return nil, err
}

func (a GovernedActionActivities) fail(ctx context.Context, input map[string]any) (map[string]any, error) {
	_, err := a.Authority.Fail(ctx,
		stringValue(input, workflows.InputTenantID),
		stringValue(input, workflows.InputCardID),
		stringValue(input, workflows.InputExecutionID),
		stringValue(input, "error_code"),
		a.Now(),
	)
	return nil, err
}

func actionFailureReason(err error) string {
	switch {
	case errors.Is(err, actions.ErrCardNotFound):
		return "card_not_found"
	case errors.Is(err, actions.ErrApprovalRequired):
		return "approval_required"
	case errors.Is(err, actions.ErrGrantRequired):
		return "grant_required"
	case errors.Is(err, actions.ErrConnectorBindingRequired):
		return "connector_binding_required"
	case errors.Is(err, actions.ErrConnectorGrantInsufficient):
		return "connector_grant_insufficient"
	case errors.Is(err, actions.ErrExecutionAdmission):
		return "execution_admission_rejected"
	case errors.Is(err, actions.ErrExecutionInProgress):
		return "execution_in_progress"
	case errors.Is(err, actions.ErrCardConflict):
		return "state_conflict"
	default:
		return ""
	}
}

func stringValue(input map[string]any, key string) string {
	value, _ := input[key].(string)
	return value
}

func optionalProjection(value any) (*actions.ConnectorBindingProjection, error) {
	if value == nil {
		return nil, nil
	}
	var projection actions.ConnectorBindingProjection
	if err := decodeObject(value, &projection); err != nil {
		return nil, fmt.Errorf("ril activities: decode connector projection: %w", err)
	}
	return &projection, nil
}

func objectMap(value any) (map[string]any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func decodeObject(value, destination any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, destination)
}
