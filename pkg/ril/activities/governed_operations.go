package activities

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/kombifyio/techstack/pkg/ril/workflow"
	"github.com/kombifyio/techstack/pkg/ril/workflows"
)

var (
	ErrOperationAuthorization = errors.New("RIL operation authorization reference is invalid")
	ErrOperationReference     = errors.New("RIL operation or compensation reference is invalid")
	ErrOperationTargets       = errors.New("RIL operation targets are invalid")
	ErrOperationIdentity      = errors.New("RIL operation identity is invalid")
)

var operationIdentityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:|\-]{0,191}$`)

// OperationInput addresses existing governed cards. It cannot supply a command,
// approval decision, grant, endpoint, agent, StackSpec or execution capability.
// IdempotencyKey is the exact key injected by the durable ActivityRunner.
type OperationInput struct {
	TenantID         string
	OwnerSubjectID   string
	Operation        string
	AuthorizationRef string
	CardID           string
	CompensationID   string
	Targets          []string
	IdempotencyKey   string
}

// GovernedOperationBackend resolves the references through the existing product
// authorities before it can dispatch. Shape admission below is not approval.
type GovernedOperationBackend interface {
	RunOperationActivity(context.Context, string, OperationInput, map[string]any) (map[string]any, error)
}

type GovernedOperationActivities struct {
	Backend GovernedOperationBackend
}

func (a GovernedOperationActivities) Register(registrar ActivityRegistrar) error {
	if registrar == nil || a.Backend == nil {
		return errors.New("RIL operational activities require a registrar and product backend")
	}
	for _, name := range []string{
		workflows.ActQueryMonitoringSignal,
		workflows.ActDispatchGovernedOperation,
		workflows.ActVerifyGovernedOperation,
		workflows.ActExecuteGovernedCompensation,
		workflows.ActRecordGovernedOperationAudit,
	} {
		registrar.RegisterActivity(name, func(ctx context.Context, input map[string]any) (map[string]any, error) {
			admitted, err := PrepareOperationInput(ctx, input)
			if err != nil {
				return nil, err
			}
			return a.Backend.RunOperationActivity(ctx, name, admitted, input)
		})
	}
	return nil
}

// PrepareOperationInput performs only deterministic reference/scope admission.
// The backend must still resolve fresh approval, grant and runtime bindings.
func PrepareOperationInput(ctx context.Context, input map[string]any) (OperationInput, error) {
	out := OperationInput{
		TenantID:         stringValue(input, workflows.InputTenantID),
		OwnerSubjectID:   stringValue(input, workflows.InputOwnerSubjectID),
		Operation:        stringValue(input, "operation"),
		AuthorizationRef: stringValue(input, workflows.InputAuthorizationRef),
		IdempotencyKey:   workflow.IdempotencyKeyFromContext(ctx),
	}
	switch out.Operation {
	case "drift-correction", "certificate-rotation", "rolling-update":
	default:
		return OperationInput{}, ErrOperationReference
	}
	var err error
	out.CardID, err = actionReference(stringValue(input, workflows.InputOperationRef))
	if err != nil {
		return OperationInput{}, err
	}
	if out.AuthorizationRef != "approval:"+out.CardID {
		return OperationInput{}, ErrOperationAuthorization
	}
	out.CompensationID, err = actionReference(stringValue(input, workflows.InputCompensationRef))
	if err != nil || out.CompensationID == out.CardID {
		return OperationInput{}, ErrOperationReference
	}
	if !operationIdentityPattern.MatchString(out.TenantID) || !operationIdentityPattern.MatchString(out.OwnerSubjectID) ||
		len(out.IdempotencyKey) < 16 || len(out.IdempotencyKey) > 128 || !operationIdentityPattern.MatchString(out.IdempotencyKey) {
		return OperationInput{}, ErrOperationIdentity
	}
	if value, present := input[workflows.InputIdempotencyKey]; present && value != out.IdempotencyKey {
		return OperationInput{}, ErrOperationIdentity
	}
	switch targets := input[workflows.InputTargets].(type) {
	case []string:
		out.Targets = append([]string(nil), targets...)
	case []any:
		for _, target := range targets {
			value, ok := target.(string)
			if !ok {
				return OperationInput{}, ErrOperationTargets
			}
			out.Targets = append(out.Targets, value)
		}
	default:
		return OperationInput{}, ErrOperationTargets
	}
	if len(out.Targets) == 0 || len(out.Targets) > 32 {
		return OperationInput{}, ErrOperationTargets
	}
	seen := make(map[string]bool, len(out.Targets))
	for _, target := range out.Targets {
		if !operationIdentityPattern.MatchString(target) || seen[target] {
			return OperationInput{}, ErrOperationTargets
		}
		seen[target] = true
	}
	return out, nil
}

func actionReference(value string) (string, error) {
	id, ok := strings.CutPrefix(value, "action:")
	if !ok || !operationIdentityPattern.MatchString(id) || strings.ContainsAny(id, ":|") {
		return "", fmt.Errorf("%w: expected a governed action-card reference", ErrOperationReference)
	}
	return id, nil
}
