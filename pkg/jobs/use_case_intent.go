package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"github.com/google/uuid"
)

var ErrNativeIntentBaselineChanged = errors.New("the installed native intent baseline changed")

// The existing pinned Guard transport authors ephemeral native candidates for
// both managed use-case changes. Runtime mutation remains Advanced's authority.
func authorNativeUseCaseCandidate(ctx context.Context, sender StackKitCommandSender, request StackKitLifecycleRequest, operation agentpb.StackKitOperation, payload []byte) (*agentpb.StackKitCommand, *agentpb.StackKitResult, error) {
	release, err := TargetStackKitReleaseForStack(ctx, request.TenantID, request.StackID)
	if err != nil {
		return nil, nil, err
	}
	if release == nil {
		return nil, nil, fmt.Errorf("native authoring requires the pinned StackKits release")
	}
	request.Operation, request.OwnerApproved = StackKitLifecyclePlan, false
	request.WizardExtend = &WizardExtendBinding{}
	command, err := stackKitLifecycleCommand("intent-"+uuid.NewString(), request, *release)
	if err != nil {
		return nil, nil, err
	}
	command.Operation = operation
	switch operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_USE_CASE_CONFIGURE:
		command.UseCaseConfigurationJson = payload
	case agentpb.StackKitOperation_STACKKIT_OPERATION_USE_CASE_REMOVE:
		command.UseCaseRemovalJson = payload
	default:
		return nil, nil, fmt.Errorf("unsupported native use-case authoring operation")
	}
	if err := stackkitcommand.ValidateCommand(command); err != nil {
		return nil, nil, err
	}
	result, err := sendStackKitCommandBoundedForTenant(ctx, sender, request.TenantID, request.AgentID, command)
	if err != nil {
		return nil, nil, err
	}
	if err := stackkitcommand.ValidateResult(result, command); err != nil {
		return nil, nil, err
	}
	if !result.Success && strings.Contains(result.Stderr, "expected spec hash does not match current intent") {
		return nil, nil, ErrNativeIntentBaselineChanged
	}
	return command, result, nil
}
