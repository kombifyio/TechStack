package stackkitcommand

import (
	"encoding/json"
	"fmt"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

type NativeIntentCandidateEvidence struct {
	ReleaseVersion   string
	PreviousSpecHash string
	SpecHash         string
	PreviousSpec     map[string]any
	CandidateSpec    map[string]any
}

// ParseNativeIntentCandidateEvidence keeps compiler hash and canonical custody
// common to the typed authoring operations. It never accepts browser candidates.
func ParseNativeIntentCandidateEvidence(c *agentpb.StackKitCommand, result *agentpb.StackKitResult, expectedHash string) (NativeIntentCandidateEvidence, error) {
	var evidence NativeIntentCandidateEvidence
	if c == nil || c.StackkitInstanceId == "" {
		return evidence, fmt.Errorf("native intent requires an exact StackInstance")
	}
	var envelope struct {
		Data struct {
			APIVersion            string          `json:"apiVersion"`
			PreviousSpecHash      string          `json:"previousSpecHash"`
			SpecHash              string          `json:"specHash"`
			Outcome               string          `json:"outcome"`
			RuntimeApplied        *bool           `json:"runtimeApplied"`
			PreviousCanonicalSpec json.RawMessage `json:"previousCanonicalSpec"`
			CandidateSpec         json.RawMessage `json:"candidate_spec"`
		} `json:"data"`
	}
	if result == nil || !result.Success || json.Unmarshal(result.CommandResultJson, &envelope) != nil {
		return evidence, fmt.Errorf("native intent authoring did not succeed")
	}
	data := envelope.Data
	if data.APIVersion != "stackkit.intent-change/v1" || data.Outcome != "candidate" || data.RuntimeApplied == nil || *data.RuntimeApplied || !specHashPattern.MatchString(data.PreviousSpecHash) || !specHashPattern.MatchString(data.SpecHash) || expectedHash != "" && data.PreviousSpecHash != expectedHash {
		return evidence, fmt.Errorf("native intent receipt is invalid or differs from the accepted baseline")
	}
	if len(data.PreviousCanonicalSpec) == 0 || len(data.PreviousCanonicalSpec) > MaxInitCandidateBytes || len(data.CandidateSpec) == 0 || len(data.CandidateSpec) > MaxInitCandidateBytes || json.Unmarshal(data.PreviousCanonicalSpec, &evidence.PreviousSpec) != nil || json.Unmarshal(data.CandidateSpec, &evidence.CandidateSpec) != nil {
		return evidence, fmt.Errorf("native intent requires the compiler's bounded canonical before and candidate")
	}
	for _, spec := range []map[string]any{evidence.PreviousSpec, evidence.CandidateSpec} {
		meta, _ := spec["metadata"].(map[string]any)
		instanceID, _ := meta["stackId"].(string)
		if instanceID == "" {
			instanceID, _ = meta["name"].(string)
		}
		if spec["apiVersion"] != "stackkit/v2alpha2" || spec["kind"] != "StackSpec" || instanceID != c.StackkitInstanceId {
			return evidence, fmt.Errorf("native intent candidate names another StackInstance")
		}
	}
	evidence.PreviousSpecHash, evidence.SpecHash = data.PreviousSpecHash, data.SpecHash
	if result.Release == nil {
		return evidence, fmt.Errorf("native intent lacks its pinned release receipt")
	}
	evidence.ReleaseVersion = result.Release.Version
	return evidence, nil
}
