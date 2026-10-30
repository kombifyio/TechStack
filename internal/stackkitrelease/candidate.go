package stackkitrelease

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/kombifyio/techstack/pkg/guardrelease"
)

// StageCandidateAssignment is native factory configuration for one synthetic
// Foundation. It is not decoded from an HTTP request or process environment.
type StageCandidateAssignment struct {
	TenantID     string
	FoundationID string
	Candidate    guardrelease.PreviewCandidateProvenance
}

// StageCandidateResolver holds authenticated immutable artifact identity.
// The Stage factory retains execution authority and must issue the existing
// per-worker signed CandidateRuntimeIntent separately before install.
type StageCandidateResolver struct {
	assignment   StageCandidateAssignment
	manifest     *guardrelease.Manifest
	bundlePath   string
	bundleSHA256 string
}

// NewStageCandidateResolver authenticates the existing Guard signature and
// entire runtime bundle before returning a Stage-only release resolver. The
// caller binds assignment to its admitted synthetic owner/tenant/Foundation.
func NewStageCandidateResolver(rawManifest, signature []byte, bundlePath string, assignment StageCandidateAssignment) (*StageCandidateResolver, error) {
	if assignment.TenantID == "" || assignment.FoundationID == "" {
		return nil, fmt.Errorf("native Stage candidate needs an exact synthetic tenant and Foundation")
	}
	if err := assignment.Candidate.Validate(); err != nil {
		return nil, err
	}
	verifier, err := guardrelease.NewConfiguredVerifier(nil)
	if err != nil {
		return nil, err
	}
	manifest, err := verifier.VerifyManifest(rawManifest, signature)
	if err != nil {
		return nil, err
	}
	if manifest.StackKits == nil || manifest.StackKits.Candidate == nil || *manifest.StackKits.Candidate != assignment.Candidate {
		return nil, fmt.Errorf("signed Guard candidate differs from native Stage assignment")
	}
	identity, ok := manifest.StackKits.ForArch("amd64")
	if !ok {
		return nil, fmt.Errorf("signed Guard candidate has no Stage linux/amd64 runtime")
	}
	file, info, err := openRegularFile(bundlePath, maxReleaseBlobBytes)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if err = guardrelease.VerifyRuntimeBundle(identity, file); err != nil {
		return nil, err
	}
	if err = ensureUnchanged(file, info); err != nil {
		return nil, err
	}
	digest, err := digestRegularFile(bundlePath, maxReleaseBlobBytes)
	if err != nil {
		return nil, err
	}
	resolver := &StageCandidateResolver{assignment: assignment, manifest: manifest, bundlePath: bundlePath, bundleSHA256: digest.sha256}
	if _, err = resolver.Resolve(); err != nil {
		return nil, err
	}
	return resolver, nil
}

func (r *StageCandidateResolver) admit(pin Pin) error {
	if r == nil || pin.SchemaVersion != CandidatePinSchemaVersion || pin.Candidate == nil || *pin.Candidate != r.assignment.Candidate {
		return fmt.Errorf("candidate pin differs from native Stage assignment")
	}
	want, ok := r.manifest.StackKits.ForArch(pin.Platform.Arch)
	if !ok || pin.Platform.OS != "linux" || pin.Version != r.manifest.StackKits.Version || pin.ArchiveSHA256 != want.ArchiveSHA256 || pin.BinarySHA256 != want.BinarySHA256 {
		return fmt.Errorf("candidate pin differs from signed Guard runtime")
	}
	return nil
}

// Resolve revalidates the full bundle, exact transfer digest and inner pin.
// Inject this method only into the native Stage job release-resolver seam.
func (r *StageCandidateResolver) Resolve() (*Release, error) {
	if r == nil {
		return nil, fmt.Errorf("native Stage candidate resolver is absent")
	}
	identity, _ := r.manifest.StackKits.ForArch("amd64")
	file, info, err := openRegularFile(r.bundlePath, maxReleaseBlobBytes)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	digest, err := digestArchiveReader(file, info.Size())
	if err != nil || digest.sha256 != r.bundleSHA256 {
		return nil, fmt.Errorf("Stage candidate bundle changed after admission")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if err = guardrelease.VerifyRuntimeBundle(identity, file); err != nil {
		return nil, err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	release, err := resolveLinuxRuntimeBundleReader(r.bundlePath, file, r.admit)
	if err != nil {
		return nil, err
	}
	if err = ensureUnchanged(file, info); err != nil {
		return nil, err
	}
	release.stageCandidate = r
	return &release, nil
}

// CandidateController consumes the already scoped opaque Stage resolver. A
// public/default Release cannot acquire candidate admission from a pathname.
func (release Release) CandidateController(runtimeDir string) (*Release, error) {
	if release.stageCandidate == nil || release.receipt.Candidate == nil {
		return nil, fmt.Errorf("scoped Stage candidate controller custody is unavailable")
	}
	return release.stageCandidate.ResolveController(runtimeDir)
}

// InstallIntent is signed using the existing worker signer after the native
// factory revalidates assignment, lease and command capability.
func (r *StageCandidateResolver) InstallIntent() (guardrelease.CandidateRuntimeIntent, error) {
	if _, err := r.Resolve(); err != nil {
		return guardrelease.CandidateRuntimeIntent{}, err
	}
	return r.manifest.CandidateIntent("amd64", r.bundleSHA256)
}

// ResolveController admits the same exact candidate tree extracted into private
// Linux Stage custody for controller-local compilation/planning. It never
// rewrites its signed pin or enables an alternate public cache entry. A Windows
// controller fails the normal platform check; its public planner stays strict.
func (r *StageCandidateResolver) ResolveController(runtimeDir string) (*Release, error) {
	if _, err := r.Resolve(); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(runtimeDir)
	if err != nil {
		return nil, err
	}
	pinPath := filepath.Join(root, "stackkits-release-pin.json")
	pin, err := LoadPin(pinPath)
	if err != nil {
		return nil, err
	}
	if err = r.admit(pin); err != nil {
		return nil, err
	}
	want, ok := r.manifest.StackKits.ForArch(pin.Platform.Arch)
	tree, err := guardrelease.TreeDigest(root)
	if err != nil || !ok || tree != want.BundleSHA256 {
		return nil, fmt.Errorf("native Stage controller tree differs from signed candidate")
	}
	// Resolve the executable at its private extraction path while retaining the
	// signed on-disk pin unchanged. The entire tree above authenticated this file.
	pin.BinaryPath = filepath.Join(root, "bin", "stackkit")
	release, err := resolveArtifactPin(pin, pinPath)
	return &release, err
}
