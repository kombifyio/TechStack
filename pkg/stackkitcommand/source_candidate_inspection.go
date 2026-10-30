package stackkitcommand

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/kombifyio/techstack/pkg/guardrelease"
)

type candidateSourceIdentity struct {
	Kit      string `json:"kit"`
	Version  string `json:"version"`
	Platform struct {
		OS   string `json:"os"`
		Arch string `json:"arch"`
	} `json:"platform"`
	IndexSHA256   string                                `json:"indexSha256"`
	ArchiveSHA256 string                                `json:"archiveSha256"`
	Source        guardrelease.PreviewCandidateSource   `json:"source"`
	Producer      guardrelease.PreviewCandidateProducer `json:"producer"`
}

// This is a projection of an already admitted native CLI result, not a new
// signature verifier, installed-cache receipt or permission to Apply/Restore.
// Only the node's canonical original Owner/member reader and offline verifier
// establish the authority represented by these bounded wire fields.
func candidateSourceInspectionVersion(raw []byte, current string) (string, bool, error) {
	var discriminator struct {
		APIVersion string `json:"apiVersion"`
	}
	if json.Unmarshal(raw, &discriminator) != nil {
		return "", false, fmt.Errorf("invalid source generation inspection")
	}
	if discriminator.APIVersion != "stackkit.source-generation-inspection/v3" {
		return "", false, nil
	}
	var source struct {
		Candidate struct {
			SchemaVersion string `json:"schemaVersion"`
			candidateSourceIdentity
			CLISHA256                string `json:"cliSha256"`
			IndexAttestationSHA256   string `json:"indexAttestationSha256"`
			PayloadAttestationSHA256 string `json:"payloadAttestationSha256"`
			TrustedRootSHA256        string `json:"trustedRootSha256"`
			SBOMSHA256               string `json:"sbomSha256"`
		} `json:"sourceCandidate"`
		Program struct {
			SchemaVersion    string                  `json:"schemaVersion"`
			ProcessRole      string                  `json:"processRole"`
			ExecutableSHA256 string                  `json:"executableSha256"`
			Candidate        candidateSourceIdentity `json:"previewCandidate"`
		} `json:"sourceProgram"`
		Public                    json.RawMessage `json:"sourceRelease"`
		OriginalExecutionIdentity string          `json:"originalExecutionIdentity"`
	}
	refuse := func() (string, bool, error) {
		return "", true, fmt.Errorf("private original source projection is inconsistent")
	}
	if json.Unmarshal(raw, &source) != nil || len(source.Public) != 0 || source.OriginalExecutionIdentity != "owner-signed-apply-program" || source.Candidate.SchemaVersion != "stackkit.preview-source-cache/v1" || source.Program.SchemaVersion != "stackkit.apply-program/v1" || source.Program.ProcessRole != "stackkit-cli" || source.Program.Candidate != source.Candidate.candidateSourceIdentity || source.Program.ExecutableSHA256 != "sha256:"+source.Candidate.CLISHA256 {
		return refuse()
	}
	proof := source.Candidate
	if !versionPattern.MatchString("v"+proof.Version) || !ReleaseAtLeast(current, "v"+proof.Version) || (proof.Kit != "basement-kit" && proof.Kit != "cloud-kit" && proof.Kit != "modern-homelab") || proof.Platform.OS != "linux" || (proof.Platform.Arch != "amd64" && proof.Platform.Arch != "arm64") {
		return refuse()
	}
	for _, digest := range []string{proof.IndexSHA256, proof.ArchiveSHA256, proof.CLISHA256, proof.IndexAttestationSHA256, proof.PayloadAttestationSHA256, proof.TrustedRootSHA256, proof.SBOMSHA256} {
		if !sha256Pattern.MatchString(digest) {
			return refuse()
		}
	}
	commit := regexp.MustCompile(`^[0-9a-f]{40}$`)
	positive := regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	if proof.Source.Repository == "" || !commit.MatchString(proof.Source.Commit) || !commit.MatchString(proof.Source.Tree) || !commit.MatchString(proof.Source.ExportTree) || proof.Producer.Workflow != ".github/workflows/release.yml" || proof.Producer.WorkflowRef != proof.Source.Repository+"/"+proof.Producer.Workflow+"@refs/heads/main" || proof.Producer.WorkflowSHA != proof.Source.Commit || proof.Producer.Event != "workflow_dispatch" || !positive.MatchString(proof.Producer.RunID) || !positive.MatchString(proof.Producer.RunAttempt) {
		return refuse()
	}
	return "v" + proof.Version, true, nil
}
