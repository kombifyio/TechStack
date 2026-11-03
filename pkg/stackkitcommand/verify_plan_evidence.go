package stackkitcommand

import (
	"encoding/json"
	"fmt"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// VerifyPlanEvidence is a projection of the verified CLI output, not an
// execution grant. Offline=true reports historical Apply evidence only.
type VerifyPlanEvidence struct {
	PlanHash           string
	ApplyResultHash    string
	EvidenceBundleHash string
	Offline            bool
}

// ParseVerifyPlanEvidence is a staged consumer for a future, separately
// qualified published release. The caller must independently admit that
// exact signed release and the current owner/server RuntimeLease; support must
// not be inferred from a version floor or the presence of these source files.
func ParseVerifyPlanEvidence(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) (VerifyPlanEvidence, error) {
	if command == nil || command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_VERIFY || !specHashPattern.MatchString(command.ExpectedPlanHash) ||
		result == nil || !result.Success || result.CommandId != command.CommandId {
		return VerifyPlanEvidence{}, fmt.Errorf("bound StackKit verify result required")
	}
	if result.ExitCode != 0 {
		return VerifyPlanEvidence{}, fmt.Errorf("StackKit verify exited unsuccessfully")
	}
	if err := requireMatchingRelease(result.Release, command.Release); err != nil {
		return VerifyPlanEvidence{}, err
	}
	if err := requirePinnedCLIEvidenceAttestation(result); err != nil {
		return VerifyPlanEvidence{}, err
	}
	var envelope struct {
		SchemaVersion string `json:"schemaVersion"`
		Command       string `json:"command"`
		Status        string `json:"status"`
		Data          struct {
			SchemaVersion string `json:"schemaVersion"`
			Offline       *bool  `json:"offline"`
			PlanHash      string `json:"planHash"`
			Apply         struct {
				PlanHash           string `json:"planHash"`
				ResultHash         string `json:"resultHash"`
				EvidenceBundleHash string `json:"evidenceBundleHash"`
			} `json:"apply"`
		} `json:"data"`
	}
	if err := json.Unmarshal(result.CommandResultJson, &envelope); err != nil {
		return VerifyPlanEvidence{}, fmt.Errorf("decode verify evidence: %w", err)
	}
	data := envelope.Data
	if envelope.SchemaVersion != CommandResultVersion || envelope.Command != ResultCommandName(command.Operation) || envelope.Status != "success" ||
		data.SchemaVersion != "stackkit.verify-result/v1" || (data.Offline == nil || *data.Offline != command.Offline) || data.PlanHash != command.ExpectedPlanHash ||
		data.Apply.PlanHash != command.ExpectedPlanHash || !specHashPattern.MatchString(data.Apply.ResultHash) || !specHashPattern.MatchString(data.Apply.EvidenceBundleHash) {
		return VerifyPlanEvidence{}, fmt.Errorf("StackKit verify evidence does not bind the approved plan and applied result")
	}
	return VerifyPlanEvidence{PlanHash: data.PlanHash, ApplyResultHash: data.Apply.ResultHash, EvidenceBundleHash: data.Apply.EvidenceBundleHash, Offline: *data.Offline}, nil
}
