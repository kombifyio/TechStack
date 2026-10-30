package agent

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// Configure is authoring only. The receiving Guard chooses a new output file
// beneath its already admitted workspace; no caller can name an output path.
func stackKitUseCaseConfigureArgs(workDir string, command *agentpb.StackKitCommand) ([]string, []string, error) {
	if err := stackkitcommand.ValidateUseCaseConfigurationFields(command); err != nil {
		return nil, nil, err
	}
	configuration, err := stackkitcommand.DecodeUseCaseConfiguration(command.UseCaseConfigurationJson)
	if err != nil {
		return nil, nil, err
	}
	directory, err := os.MkdirTemp(workDir, ".techstack-configure-")
	if err != nil {
		return nil, nil, err
	}
	candidate := filepath.Join(directory, "candidate.json")
	files := []string{candidate, directory}
	args := []string{"use-cases", "configure", configuration.WorkloadSlug, "--json", "--output", candidate}
	if configuration.ExpectedSpecHash != "" {
		args = append(args, "--expected-spec-hash", configuration.ExpectedSpecHash)
	}
	for _, option := range []struct{ flag, value string }{{"--compute-profile", configuration.ComputeProfile}, {"--storage-profile", configuration.StorageProfile}, {"--accelerator-profile", configuration.AcceleratorProfile}} {
		if option.value != "" {
			args = append(args, option.flag, option.value)
		}
	}
	keys := make([]string, 0, len(configuration.Settings))
	for key := range configuration.Settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--setting", key+"="+string(configuration.Settings[key]))
	}
	keys = keys[:0]
	for key := range configuration.SecretRefs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--secret-ref", key+"="+configuration.SecretRefs[key])
	}
	return args, files, nil
}

func attachUseCaseConfigureCandidate(command *agentpb.StackKitCommand, result *agentpb.StackKitResult, tempFiles []string) error {
	if err := attachStackKitIntentCandidate(result, tempFiles, ".techstack-configure-"); err != nil {
		return err
	}
	_, err := stackkitcommand.ParseUseCaseConfigurationEvidence(command, result)
	return err
}
