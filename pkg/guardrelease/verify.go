package guardrelease

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"time"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// GitHubOIDCIssuer is the OIDC issuer of keyless signatures made by GitHub
// Actions.
const GitHubOIDCIssuer = "https://token.actions.githubusercontent.com"

// CodeDowngrade is reported for a candidate older than the installed version.
const CodeDowngrade = "downgrade_refused"

// pinnedTrustedRoot is the Sigstore public-good trusted root the Guard verifies
// against. It is pinned in the binary so verification needs no network and no
// trust-on-first-use; a new root ships with a new signed Guard release.
//
//go:embed sigstore-trusted-root.json
var pinnedTrustedRoot []byte

// Identity is the signer a release must come from.
type Identity struct {
	// Issuer is the exact OIDC issuer of the signing certificate.
	Issuer string
	// SANRegexp must match the certificate's subject alternative name: the URI
	// of the release workflow and its ref.
	SANRegexp string
	// Extensions pins Fulcio certificate extensions. Every non-empty field must
	// equal the certificate's value exactly; empty fields are not checked.
	Extensions certificate.Extensions
	// BuildTriggers lists the events allowed to have produced the signature
	// (Fulcio "Build Trigger"). Empty means the trigger is not checked.
	BuildTriggers []string
}

const (
	releaseRepositoryURI = "https://github.com/kombifyio/TechStack"
	releaseWorkflowURI   = releaseRepositoryURI + "/.github/workflows/guard-release.yml@refs/heads/main"
)

// DefaultIdentity is the release workflow of this repository on its main branch
// and nothing else: not a tag, not a fork, not a branch. The subject is anchored
// and exact, and the certificate must also say it came from this repository's
// main ref, from the workflow file itself, on a GitHub-hosted runner, started by
// a manual dispatch or the nightly schedule. A signature obtained any other way
// (for example from a workflow run at a tag or from a self-hosted runner) is
// refused even when the subject matched.
var DefaultIdentity = Identity{
	Issuer:    GitHubOIDCIssuer,
	SANRegexp: `^https://github\.com/kombifyio/TechStack/\.github/workflows/guard-release\.yml@refs/heads/main$`,
	Extensions: certificate.Extensions{
		SourceRepositoryURI:      releaseRepositoryURI,
		SourceRepositoryOwnerURI: "https://github.com/kombifyio",
		SourceRepositoryRef:      "refs/heads/main",
		BuildSignerURI:           releaseWorkflowURI,
		RunnerEnvironment:        "github-hosted",
	},
	BuildTriggers: []string{"workflow_dispatch", "schedule"},
}

// Options configure a Verifier. The zero value verifies against the pinned root
// and the pinned identity (DefaultIdentity) with the wall clock.
type Options struct {
	// TrustedRootJSON replaces the pinned trusted root (tests, private
	// Sigstore deployments).
	TrustedRootJSON []byte
	Identity        Identity
	Now             func() time.Time

	// material is a test seam for an in-memory Sigstore; its certificates carry
	// no embedded certificate timestamps, so those are not required with it.
	material root.TrustedMaterial
}

// Verifier authenticates manifests and artifacts.
type Verifier struct {
	verifier *verify.Verifier
	identity verify.CertificateIdentity
	triggers []string
	now      func() time.Time
}

// NewVerifier builds a Verifier. An invalid identity or trusted root is an error:
// there is no permissive fallback.
func NewVerifier(opts Options) (*Verifier, error) {
	identity := opts.Identity
	if identity.Issuer == "" && identity.SANRegexp == "" {
		pinned, err := pinnedIdentity()
		if err != nil {
			return nil, err
		}
		identity = pinned
	}
	if identity.Issuer == "" || identity.SANRegexp == "" {
		return nil, errors.New("a release signer needs an issuer and a subject pattern")
	}
	if _, err := regexp.Compile(identity.SANRegexp); err != nil {
		return nil, fmt.Errorf("release signer subject pattern: %w", err)
	}
	material := opts.material
	if material == nil {
		rootJSON := opts.TrustedRootJSON
		if len(rootJSON) == 0 {
			rootJSON = pinnedTrustedRoot
		}
		trusted, err := root.NewTrustedRootFromJSON(rootJSON)
		if err != nil {
			return nil, fmt.Errorf("load the pinned Sigstore trusted root: %w", err)
		}
		material = trusted
	}
	certificateIdentity, err := newCertificateIdentity(identity)
	if err != nil {
		return nil, fmt.Errorf("release signer identity: %w", err)
	}
	verifierOptions := []verify.VerifierOption{verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1)}
	if opts.material == nil {
		verifierOptions = append(verifierOptions, verify.WithSignedCertificateTimestamps(1))
	}
	inner, err := verify.NewVerifier(material, verifierOptions...)
	if err != nil {
		return nil, fmt.Errorf("configure the release verifier: %w", err)
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Verifier{verifier: inner, identity: certificateIdentity, triggers: append([]string(nil), identity.BuildTriggers...), now: now}, nil
}

// VerifyManifest authenticates a manifest and checks that it is fresh. The
// returned manifest is trusted; the caller must use it, never a re-parse of raw.
func (v *Verifier) VerifyManifest(raw, bundleJSON []byte) (*Manifest, error) {
	if len(bundleJSON) == 0 {
		return nil, &Denial{CodeEvidenceMissing, "the release manifest has no signature bundle"}
	}
	entity, err := decodeBundle(bundleJSON)
	if err != nil {
		return nil, err
	}
	return v.verifyManifestEntity(raw, entity)
}

func (v *Verifier) verifyManifestEntity(raw []byte, entity verify.SignedEntity) (*Manifest, error) {
	manifest, err := ParseManifest(raw)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	if err := v.verifyEntity(entity, sum[:]); err != nil {
		return nil, err
	}
	if err := manifest.Fresh(v.now()); err != nil {
		return nil, &Denial{CodeManifestStale, err.Error()}
	}
	return manifest, nil
}

// Evidence describes an artifact that passed verification.
type Evidence struct {
	Version string
	SHA256  string
	Size    int64
}

// VerifyArtifact authenticates one artifact file against a verified manifest.
// The file must be a root-owned copy nothing else can modify while it is read:
// the digest is computed from the same bytes the caller then installs.
// installedVersion, when set, refuses a downgrade.
func (v *Verifier) VerifyArtifact(manifest *Manifest, goos, goarch, path string, bundleJSON []byte, installedVersion string) (Evidence, error) {
	if len(bundleJSON) == 0 {
		return Evidence{}, &Denial{CodeEvidenceMissing, "the artifact has no signature bundle"}
	}
	entity, err := decodeBundle(bundleJSON)
	if err != nil {
		return Evidence{}, err
	}
	return v.verifyArtifactEntity(manifest, goos, goarch, path, entity, installedVersion)
}

func (v *Verifier) verifyArtifactEntity(manifest *Manifest, goos, goarch, path string, entity verify.SignedEntity, installedVersion string) (Evidence, error) {
	if manifest == nil {
		return Evidence{}, &Denial{CodeEvidenceMissing, "no verified release manifest"}
	}
	if err := manifest.Fresh(v.now()); err != nil {
		return Evidence{}, &Denial{CodeManifestStale, err.Error()}
	}
	entry, ok := manifest.Artifact(goos, goarch)
	if !ok {
		return Evidence{}, &Denial{CodeNotInManifest, fmt.Sprintf("the release has no %s/%s artifact", goos, goarch)}
	}
	sum, size, err := digestFile(path)
	if err != nil {
		return Evidence{}, err
	}
	digest := hex.EncodeToString(sum)
	if digest != entry.SHA256 || size != entry.Size {
		return Evidence{}, &Denial{CodeDigestMismatch, "the artifact does not match the digest the signed manifest names"}
	}
	if err := v.verifyEntity(entity, sum); err != nil {
		return Evidence{}, err
	}
	if denial := manifest.Admit(manifest.Version, digest); denial != nil {
		return Evidence{}, denial
	}
	if denial := manifest.CheckDowngrade(installedVersion); denial != nil {
		return Evidence{}, denial
	}
	return Evidence{Version: manifest.Version, SHA256: digest, Size: size}, nil
}

// CheckDowngrade refuses a manifest whose release is older than the installed
// version. An empty installedVersion disables the check.
func (m *Manifest) CheckDowngrade(installedVersion string) *Denial {
	if installedVersion == "" {
		return nil
	}
	if order, ok := CompareVersions(m.Version, installedVersion); ok && order < 0 {
		return &Denial{CodeDowngrade, fmt.Sprintf("version %s is older than the installed %s", m.Version, installedVersion)}
	}
	return nil
}

func decodeBundle(bundleJSON []byte) (verify.SignedEntity, error) {
	if len(bundleJSON) > MaxBundleBytes {
		return nil, &Denial{CodeSignatureInvalid, "the signature bundle exceeds its size bound"}
	}
	parsed := &bundle.Bundle{}
	if err := parsed.UnmarshalJSON(bundleJSON); err != nil {
		return nil, &Denial{CodeSignatureInvalid, "the signature bundle cannot be decoded"}
	}
	return parsed, nil
}

// newCertificateIdentity turns a pinned Identity into the matcher the
// certificate is checked against: an anchored subject, an exact issuer and the
// exact Fulcio extensions.
func newCertificateIdentity(identity Identity) (verify.CertificateIdentity, error) {
	sanMatcher, err := verify.NewSANMatcher("", identity.SANRegexp)
	if err != nil {
		return verify.CertificateIdentity{}, err
	}
	issuerMatcher, err := verify.NewIssuerMatcher(identity.Issuer, "")
	if err != nil {
		return verify.CertificateIdentity{}, err
	}
	return verify.NewCertificateIdentity(sanMatcher, issuerMatcher, identity.Extensions)
}

func (v *Verifier) verifyEntity(entity verify.SignedEntity, digest []byte) error {
	policy := verify.NewPolicy(verify.WithArtifactDigest("sha256", digest), verify.WithCertificateIdentity(v.identity))
	result, err := v.verifier.Verify(entity, policy)
	if err != nil {
		return &Denial{CodeSignatureInvalid, "the signature is not valid for this artifact from the pinned release signer"}
	}
	if err := v.checkTrigger(result); err != nil {
		return &Denial{CodeSignatureInvalid, "the signature is not valid for this artifact from the pinned release signer"}
	}
	return nil
}

// checkTrigger requires the build trigger of the signing certificate to be one
// the pinned identity permits. The certificate library matches an extension
// against one exact value, so a set of triggers is checked here, on the
// certificate the verified signature was made with.
func (v *Verifier) checkTrigger(result *verify.VerificationResult) error {
	if len(v.triggers) == 0 {
		return nil
	}
	if result == nil || result.Signature == nil || result.Signature.Certificate == nil {
		return errors.New("the signature carries no certificate to check the build trigger of")
	}
	return checkBuildTrigger(v.triggers, result.Signature.Certificate.BuildTrigger)
}

func checkBuildTrigger(allowed []string, trigger string) error {
	for _, candidate := range allowed {
		if trigger == candidate {
			return nil
		}
	}
	return fmt.Errorf("build trigger %q is not permitted", trigger)
}

func digestFile(path string) ([]byte, int64, error) {
	file, err := os.Open(path) // #nosec G304 -- the caller passes its own root-owned staging path.
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, 0, errors.New("the artifact is not a regular file")
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return nil, 0, err
	}
	return hash.Sum(nil), size, nil
}
