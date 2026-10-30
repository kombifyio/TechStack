package jobs

import (
	"context"
	"encoding/json"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

var ErrUseCaseConfigurationBaselineChanged = ErrNativeIntentBaselineChanged

func AuthorUseCaseConfiguration(ctx context.Context, sender StackKitCommandSender, request StackKitLifecycleRequest, configuration stackkitcommand.UseCaseConfiguration) (stackkitcommand.UseCaseConfigurationEvidence, error) {
	var empty stackkitcommand.UseCaseConfigurationEvidence
	raw, err := json.Marshal(configuration)
	if err != nil {
		return empty, err
	}
	command, result, err := authorNativeUseCaseCandidate(ctx, sender, request, agentpb.StackKitOperation_STACKKIT_OPERATION_USE_CASE_CONFIGURE, raw)
	if err != nil {
		return empty, err
	}
	return stackkitcommand.ParseUseCaseConfigurationEvidence(command, result)
}
