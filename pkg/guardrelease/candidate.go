package guardrelease

import (
	"errors"
	"regexp"
)

const PreviewCandidateProvenanceSchema = "stackkits.preview-candidate-provenance/v1"

// PreviewCandidateProvenance identifies a private Actions artifact whose index
// and payload the existing Guard producer authenticated before signing its tree.
// This is provenance, never authority to install on a host.
type PreviewCandidateProvenance struct {
	SchemaVersion            string                   `json:"schemaVersion"`
	IndexSHA256              string                   `json:"indexSha256"`
	IndexAttestationSHA256   string                   `json:"indexAttestationSha256"`
	PayloadAttestationSHA256 string                   `json:"payloadAttestationSha256"`
	TrustedRootSHA256        string                   `json:"trustedRootSha256"`
	Artifact                 PreviewCandidateArtifact `json:"artifact"`
	Source                   PreviewCandidateSource   `json:"source"`
	Producer                 PreviewCandidateProducer `json:"producer"`
}
type PreviewCandidateArtifact struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}
type PreviewCandidateSource struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
	Tree       string `json:"tree"`
	ExportTree string `json:"exportTree"`
}
type PreviewCandidateProducer struct {
	Workflow    string `json:"workflow"`
	WorkflowRef string `json:"workflowRef"`
	WorkflowSHA string `json:"workflowSha"`
	RunID       string `json:"runId"`
	RunAttempt  string `json:"runAttempt"`
	Event       string `json:"event"`
}

var candidateCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var candidateIDPattern = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

// Validate checks the discriminator and immutable identity, not the signature.
func (c PreviewCandidateProvenance) Validate() error {
	return errors.New("private Preview candidate provenance is unavailable in public builds")
}

// CandidateRuntimeIntent is the exact install payload signed by the existing
// per-worker command-binding signer. It contains no paths, argv or environment.
type CandidateRuntimeIntent struct {
	Candidate     PreviewCandidateProvenance `json:"candidate"`
	Version       string                     `json:"version"`
	OS            string                     `json:"os"`
	Arch          string                     `json:"arch"`
	ArchiveSHA256 string                     `json:"archiveSha256"`
	BinarySHA256  string                     `json:"binarySha256"`
	ServerSHA256  string                     `json:"serverSha256"`
	TreeSHA256    string                     `json:"treeSha256"`
	BundleSHA256  string                     `json:"bundleSha256"`
}

// CandidateIntent derives a request from an already authenticated manifest.
// The native Stage factory must bind this request to its exact synthetic
// assignment before its existing per-worker signer signs it.
func (m *Manifest) CandidateIntent(arch, bundleSHA256 string) (CandidateRuntimeIntent, error) {
	if m == nil || m.StackKits == nil || m.StackKits.Candidate == nil {
		return CandidateRuntimeIntent{}, errors.New("signed manifest names no internal StackKits candidate")
	}
	if err := m.validateShape(); err != nil {
		return CandidateRuntimeIntent{}, err
	}
	r, ok := m.StackKits.ForArch(arch)
	if !ok || r.BundleSHA256 == "" || !digestPattern.MatchString(bundleSHA256) {
		return CandidateRuntimeIntent{}, errors.New("candidate runtime platform/bundle digest is incomplete")
	}
	return CandidateRuntimeIntent{Candidate: *m.StackKits.Candidate, Version: m.StackKits.Version, OS: "linux", Arch: arch,
		ArchiveSHA256: r.ArchiveSHA256, BinarySHA256: r.BinarySHA256, ServerSHA256: r.ServerSHA256, TreeSHA256: r.BundleSHA256, BundleSHA256: bundleSHA256}, nil
}
