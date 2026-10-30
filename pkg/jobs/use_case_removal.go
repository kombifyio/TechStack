package jobs

import (
	"context"
	"encoding/json"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

func AuthorUseCaseRemoval(ctx context.Context, sender StackKitCommandSender, request StackKitLifecycleRequest, removal stackkitcommand.UseCaseRemoval) (stackkitcommand.UseCaseRemovalEvidence, error) {
	var empty stackkitcommand.UseCaseRemovalEvidence
	raw, err := json.Marshal(removal)
	if err != nil {
		return empty, err
	}
	command, result, err := authorNativeUseCaseCandidate(ctx, sender, request, agentpb.StackKitOperation_STACKKIT_OPERATION_USE_CASE_REMOVE, raw)
	if err != nil {
		return empty, err
	}
	return stackkitcommand.ParseUseCaseRemovalEvidence(command, result)
}

type WizardUseCaseRemovalBinding struct {
	Intent   stackkitcommand.UseCaseRemoval `json:"intent"`
	SpecHash string                         `json:"spec_hash"`
}
