package agent

import (
	"os"
	"path/filepath"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

func stackKitUseCaseRemoveArgs(workDir string, command *agentpb.StackKitCommand) ([]string, []string, error) {
	if err := stackkitcommand.ValidateUseCaseRemovalFields(command); err != nil {
		return nil, nil, err
	}
	removal, err := stackkitcommand.DecodeUseCaseRemoval(command.UseCaseRemovalJson)
	if err != nil {
		return nil, nil, err
	}
	directory, err := os.MkdirTemp(workDir, ".techstack-remove-")
	if err != nil {
		return nil, nil, err
	}
	candidate := filepath.Join(directory, "candidate.json")
	selector := "--workload"
	if removal.Kind == stackkitcommand.UseCaseRemovalGroup {
		selector = "--use-case"
	}
	return []string{"remove", selector, removal.Ref, "--json", "--candidate-output", candidate, "--expected-spec-hash", removal.ExpectedSpecHash}, []string{candidate, directory}, nil
}

func attachUseCaseRemoveCandidate(command *agentpb.StackKitCommand, result *agentpb.StackKitResult, tempFiles []string) error {
	if err := attachStackKitIntentCandidate(result, tempFiles, ".techstack-remove-"); err != nil {
		return err
	}
	_, err := stackkitcommand.ParseUseCaseRemovalEvidence(command, result)
	return err
}
