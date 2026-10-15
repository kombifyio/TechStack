package stackkitcommand

import (
	"encoding/json"
	"fmt"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// ManagedSourceGenerationBackupPlan admits preparation provenance only. The
// historical source, verified by StackKits' installed-release framework, is
// retained without changing its compiler or making it an Apply/Restore plan.
func ManagedSourceGenerationBackupPlan(result *agentpb.StackKitResult, instance string) ([]byte, string, error) {
	plan, hash, err := ManagedBackupPlan(result, instance)
	if err != nil {
		return nil, "", err
	}
	var envelope struct {
		Data struct {
			Output    string          `json:"output"`
			Authority json.RawMessage `json:"source_generation_authority"`
		} `json:"data"`
	}
	if result.Release == nil || json.Unmarshal(result.CommandResultJson, &envelope) != nil {
		return nil, "", fmt.Errorf("source generation release is unavailable")
	}
	if len(envelope.Data.Output) == 0 || len(envelope.Data.Output) > 2<<20 || len(envelope.Data.Authority) > 2<<20 {
		return nil, "", fmt.Errorf("source generation provenance is not bounded")
	}
	var inspection struct {
		Binding struct {
			PlanHash        string `json:"planHash"`
			CompilerVersion string `json:"compilerVersion"`
			Renderer        struct {
				Version string `json:"version"`
			} `json:"renderer"`
		} `json:"binding"`
	}
	if json.Unmarshal([]byte(envelope.Data.Output), &inspection) != nil {
		return nil, "", fmt.Errorf("source generation binding is invalid")
	}
	current := result.Release.Version
	if !versionPattern.MatchString(current) {
		return nil, "", fmt.Errorf("source generation current release is invalid")
	}
	if len(envelope.Data.Authority) == 0 {
		if inspection.Binding.CompilerVersion != "stackkits-resolver/"+current[1:] || inspection.Binding.Renderer.Version != current[1:] {
			return nil, "", fmt.Errorf("historical generation requires installed-source proof")
		}
		return plan, hash, nil
	}
	var source struct {
		APIVersion string          `json:"apiVersion"`
		Inspection json.RawMessage `json:"inspection"`
		Release    struct {
			SchemaVersion string `json:"schemaVersion"`
			Kit           string `json:"kit"`
			Version       string `json:"version"`
			Channel       string `json:"channel"`
			ArchiveSHA256 string `json:"archiveSha256"`
			IndexSHA256   string `json:"indexSha256"`
		} `json:"sourceRelease"`
		Owner struct {
			OwnerRef        string `json:"ownerRef"`
			KeyID           string `json:"keyId"`
			PocketIDSubject string `json:"pocketIdSubject"`
			BindingDigest   string `json:"ownerBindingDigest"`
		} `json:"owner"`
		ApplyResultHash    string `json:"applyResultHash"`
		EvidenceBundleHash string `json:"evidenceBundleHash"`
	}
	if json.Unmarshal(envelope.Data.Authority, &source) != nil || source.APIVersion != "stackkit.source-generation-inspection/v1" || source.Release.SchemaVersion != "stackkit.release-receipt/v1" || source.Release.Kit != "cloud-kit" || source.Release.Channel != "stable" || !versionPattern.MatchString(source.Release.Version) || ReleaseAtLeast(source.Release.Version, current) || !sha256Pattern.MatchString(source.Release.ArchiveSHA256) || !sha256Pattern.MatchString(source.Release.IndexSHA256) || source.Owner.OwnerRef == "" || source.Owner.KeyID == "" || source.Owner.PocketIDSubject == "" || !specHashPattern.MatchString(source.Owner.BindingDigest) || !specHashPattern.MatchString(source.ApplyResultHash) || !specHashPattern.MatchString(source.EvidenceBundleHash) {
		return nil, "", fmt.Errorf("source generation proof is outside attested applied custody")
	}
	var historicalInspection any
	var retainedInspection any
	if json.Unmarshal(source.Inspection, &historicalInspection) != nil || json.Unmarshal([]byte(envelope.Data.Output), &retainedInspection) != nil {
		return nil, "", fmt.Errorf("source generation inspection proof is invalid")
	}
	first, _ := json.Marshal(historicalInspection)
	second, _ := json.Marshal(retainedInspection)
	if string(first) != string(second) || inspection.Binding.CompilerVersion != "stackkits-resolver/"+source.Release.Version[1:] || inspection.Binding.Renderer.Version != source.Release.Version[1:] {
		return nil, "", fmt.Errorf("source generation proof differs from its retained Plan")
	}
	return plan, hash, nil
}
