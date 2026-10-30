// Package guardrelease decides whether a kombify Guard artifact may be
// installed. It authenticates the artifact, never the channel it arrived on: a
// checksum served next to a binary proves transfer integrity only.
//
// An artifact is admitted when all of the following hold:
//
//   - a release manifest names the artifact's exact digest and its platform;
//   - the manifest and the artifact each carry a Sigstore bundle whose
//     certificate identity is the pinned release workflow, verified against the
//     pinned trusted root (no network, no TUF refresh at install time);
//   - the manifest is fresh: issued in the past and not expired;
//   - the version is not below the manifest's minimum admitted version and
//     neither the version nor the digest is revoked.
//
// Missing or stale trust evidence denies the update; it is never skipped.
package guardrelease

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// ManifestSchema names the signed release manifest document.
	ManifestSchema = "guard.release-manifest/v1"
	// MaxManifestBytes bounds a manifest read from disk or the wire.
	MaxManifestBytes = 256 << 10
	// MaxBundleBytes bounds one Sigstore bundle.
	MaxBundleBytes = 1 << 20
	// MaxManifestLifetime bounds how long one signed manifest may be valid. It is
	// the validity the release workflow signs; a longer lifetime would let a stale
	// manifest outlive a revocation.
	MaxManifestLifetime = 14 * 24 * time.Hour
	// clockSkew is the tolerated difference between the issuing and the local clock.
	clockSkew = 10 * time.Minute
)

var (
	versionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	digestPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	platformToken  = regexp.MustCompile(`^[a-z0-9_]{1,16}$`)
)

// Manifest is the signed release metadata of one Guard release.
type Manifest struct {
	SchemaVersion string `json:"schemaVersion"`
	// Version is the exact release every artifact in the manifest belongs to.
	Version string `json:"version"`
	// MinimumAdmittedVersion is the lowest version that may run or be installed.
	MinimumAdmittedVersion string `json:"minimumAdmittedVersion"`
	// RevokedVersions and RevokedDigests are never admitted, even when they are
	// at or above the minimum.
	RevokedVersions []string   `json:"revokedVersions"`
	RevokedDigests  []string   `json:"revokedDigests"`
	Artifacts       []Artifact `json:"artifacts"`
	// StackKits names the StackKits runtime this release's control plane
	// publishes. The privileged executor installs a StackKits runtime bundle
	// only when its executables match these digests, so a bundle handed over by
	// the unprivileged daemon cannot bring its own code into root's hands.
	StackKits *StackKitsRuntime `json:"stackkits,omitempty"`
	IssuedAt  time.Time         `json:"issuedAt"`
	ExpiresAt time.Time         `json:"expiresAt"`
}

// StackKitsRuntime is the pinned StackKits runtime of a release. The top-level
// digests describe the linux/amd64 runtime; LinuxArm64 describes linux/arm64.
type StackKitsRuntime struct {
	// Candidate is present only in an Actions-only internal Preview candidate.
	Candidate     *PreviewCandidateProvenance `json:"candidate,omitempty"`
	Version       string                      `json:"version"`
	ArchiveSHA256 string                      `json:"archiveSha256"`
	// BinarySHA256 and ServerSHA256 are the digests of the `stackkit` and
	// `stackkit-server` executables inside the published archive.
	BinarySHA256 string `json:"binarySha256"`
	ServerSHA256 string `json:"serverSha256"`
	// BundleSHA256 is the canonical digest of the whole runtime bundle tree
	// (see TreeDigest): every file that root may read or run, not only the
	// executables. The privileged executor refuses a bundle whose tree differs,
	// and one whose manifest names no tree digest.
	BundleSHA256 string `json:"bundleSha256,omitempty"`
	// LinuxArm64 is the same identity for linux/arm64 hosts.
	LinuxArm64 *StackKitsArchRuntime `json:"linuxArm64,omitempty"`
}

// StackKitsArchRuntime is the StackKits runtime identity of one architecture.
type StackKitsArchRuntime struct {
	ArchiveSHA256 string `json:"archiveSha256"`
	BinarySHA256  string `json:"binarySha256"`
	ServerSHA256  string `json:"serverSha256"`
	BundleSHA256  string `json:"bundleSha256,omitempty"`
}

// ForArch returns the runtime identity a linux host of goarch must install.
func (r *StackKitsRuntime) ForArch(goarch string) (StackKitsArchRuntime, bool) {
	switch goarch {
	case "amd64":
		return StackKitsArchRuntime{ArchiveSHA256: r.ArchiveSHA256, BinarySHA256: r.BinarySHA256, ServerSHA256: r.ServerSHA256, BundleSHA256: r.BundleSHA256}, true
	case "arm64":
		if r.LinuxArm64 != nil {
			return *r.LinuxArm64, true
		}
	}
	return StackKitsArchRuntime{}, false
}

// valid checks the shape only. A manifest signed before bundle digests existed
// still parses, so the control plane can serve it; the executor then refuses any
// runtime install for lack of a tree digest.
func (a StackKitsArchRuntime) valid() bool {
	return digestPattern.MatchString(a.ArchiveSHA256) && digestPattern.MatchString(a.BinarySHA256) &&
		digestPattern.MatchString(a.ServerSHA256) && (a.BundleSHA256 == "" || digestPattern.MatchString(a.BundleSHA256))
}

// Artifact names one signed binary of a release.
type Artifact struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// ParseManifest decodes a manifest strictly and validates its shape. It does not
// authenticate it: use Verifier.VerifyManifest for that.
func ParseManifest(raw []byte) (*Manifest, error) {
	if len(raw) == 0 || len(raw) > MaxManifestBytes {
		return nil, errors.New("release manifest is empty or exceeds its size bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode release manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("release manifest has trailing data")
	}
	if err := manifest.validateShape(); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func (m *Manifest) validateShape() error {
	if m.SchemaVersion != ManifestSchema {
		return fmt.Errorf("release manifest schema %q is not %s", m.SchemaVersion, ManifestSchema)
	}
	if _, ok := parseVersion(m.Version); !ok {
		return fmt.Errorf("release manifest version %q is not X.Y.Z", m.Version)
	}
	if _, ok := parseVersion(m.MinimumAdmittedVersion); !ok {
		return fmt.Errorf("release manifest minimum admitted version %q is not X.Y.Z", m.MinimumAdmittedVersion)
	}
	for _, version := range m.RevokedVersions {
		if _, ok := parseVersion(version); !ok {
			return fmt.Errorf("revoked version %q is not X.Y.Z", version)
		}
	}
	for _, digest := range m.RevokedDigests {
		if !digestPattern.MatchString(digest) {
			return fmt.Errorf("revoked digest %q is not a lowercase sha256", digest)
		}
	}
	if len(m.Artifacts) == 0 || len(m.Artifacts) > 16 {
		return errors.New("release manifest must list between one and sixteen artifacts")
	}
	seen := map[string]bool{}
	for _, artifact := range m.Artifacts {
		key := artifact.OS + "/" + artifact.Arch
		if !platformToken.MatchString(artifact.OS) || !platformToken.MatchString(artifact.Arch) || seen[key] ||
			!digestPattern.MatchString(artifact.SHA256) || artifact.Size <= 0 {
			return fmt.Errorf("release manifest artifact %q is invalid or duplicated", key)
		}
		seen[key] = true
	}
	if m.StackKits != nil {
		if m.StackKits.Candidate != nil {
			if err := m.StackKits.Candidate.Validate(); err != nil {
				return err
			}
		}
		amd64, _ := m.StackKits.ForArch("amd64")
		if strings.TrimSpace(m.StackKits.Version) == "" || !amd64.valid() ||
			(m.StackKits.LinuxArm64 != nil && !m.StackKits.LinuxArm64.valid()) {
			return errors.New("release manifest StackKits runtime is incomplete")
		}
	}
	if m.IssuedAt.IsZero() || m.ExpiresAt.IsZero() || !m.ExpiresAt.After(m.IssuedAt) || m.ExpiresAt.Sub(m.IssuedAt) > MaxManifestLifetime {
		return fmt.Errorf("release manifest validity window is missing or longer than %s", MaxManifestLifetime)
	}
	return nil
}

// Fresh reports whether the manifest may be relied on at now.
func (m *Manifest) Fresh(now time.Time) error {
	if now.Add(clockSkew).Before(m.IssuedAt) {
		return errors.New("release manifest is issued in the future")
	}
	if !now.Before(m.ExpiresAt) {
		return fmt.Errorf("release manifest expired at %s", m.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return nil
}

// Artifact returns the entry for one platform.
func (m *Manifest) Artifact(goos, goarch string) (Artifact, bool) {
	for _, artifact := range m.Artifacts {
		if artifact.OS == goos && artifact.Arch == goarch {
			return artifact, true
		}
	}
	return Artifact{}, false
}

// Denial explains why a version or digest is not admitted.
type Denial struct {
	Code    string
	Message string
}

func (d *Denial) Error() string { return d.Code + ": " + d.Message }

// Stable denial codes; the executor reports them and the control plane shows them.
const (
	CodeRevoked          = "artifact_revoked"
	CodeBelowMinimum     = "below_minimum_version"
	CodeDigestMismatch   = "artifact_digest_mismatch"
	CodeNotInManifest    = "artifact_not_in_manifest"
	CodeManifestStale    = "manifest_stale"
	CodeSignatureInvalid = "signature_invalid"
	CodeEvidenceMissing  = "trust_evidence_missing"
	// CodeManifestSuperseded is reported for a manifest older than one already
	// accepted on this host.
	CodeManifestSuperseded = "manifest_superseded"
)

// Admit applies the manifest's version policy to one version and digest. The
// digest is lowercase hex.
func (m *Manifest) Admit(version, digest string) *Denial {
	return admit(m.MinimumAdmittedVersion, m.RevokedVersions, m.RevokedDigests, version, digest)
}

func admit(minimumAdmitted string, revokedVersions, revokedDigests []string, version, digest string) *Denial {
	got, ok := parseVersion(version)
	if !ok {
		return &Denial{CodeBelowMinimum, fmt.Sprintf("version %q is not X.Y.Z", version)}
	}
	for _, revoked := range revokedVersions {
		if want, _ := parseVersion(revoked); want == got {
			return &Denial{CodeRevoked, fmt.Sprintf("version %s is revoked", version)}
		}
	}
	for _, revoked := range revokedDigests {
		if revoked == strings.ToLower(digest) {
			return &Denial{CodeRevoked, "the artifact digest is revoked"}
		}
	}
	if minimum, ok := parseVersion(minimumAdmitted); ok && compareParsed(got, minimum) < 0 {
		return &Denial{CodeBelowMinimum, fmt.Sprintf("version %s is below the minimum admitted version %s", version, minimumAdmitted)}
	}
	return nil
}

// CompareVersions orders two X.Y.Z versions; ok is false when either is invalid.
func CompareVersions(a, b string) (order int, ok bool) {
	left, leftOK := parseVersion(a)
	right, rightOK := parseVersion(b)
	if !leftOK || !rightOK {
		return 0, false
	}
	return compareParsed(left, right), true
}

func parseVersion(version string) ([3]int, bool) {
	match := versionPattern.FindStringSubmatch(strings.TrimSpace(version))
	if match == nil {
		return [3]int{}, false
	}
	var parts [3]int
	for i := range parts {
		value, err := strconv.Atoi(match[i+1])
		if err != nil {
			return [3]int{}, false
		}
		parts[i] = value
	}
	return parts, true
}

func compareParsed(a, b [3]int) int {
	for i := range a {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}

// DecodeDigest returns the raw bytes of a lowercase hex sha256.
func DecodeDigest(digest string) ([]byte, error) {
	if !digestPattern.MatchString(digest) {
		return nil, errors.New("digest is not a lowercase sha256")
	}
	return hex.DecodeString(digest)
}
