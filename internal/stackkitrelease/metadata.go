package stackkitrelease

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/guardrelease"
)

// ResolveLinuxRuntimeMetadata preserves the managed node's signed release
// identity without retaining its distribution archive in the control plane.
// The image build verifies the complete signed release and extracts only these
// bounded files from that verified bundle. This function rejects drift between
// the extracted pin and the signed seed manifest on every execution.
func ResolveLinuxRuntimeMetadata(dir string) (Release, error) {
	pinPath := filepath.Join(dir, "stackkits-release-pin.json")
	pinRaw, err := readBoundedRegularFile(pinPath, maxPinBytes)
	if err != nil {
		return Release{}, err
	}
	var pin Pin
	if err := decodeStrict(pinRaw, &pin); err != nil {
		return Release{}, err
	}
	if err := pin.validateForPlatform(Platform{OS: "linux", Arch: "amd64"}); err != nil {
		return Release{}, err
	}
	if pin.SchemaVersion != PinSchemaVersion || pin.BinaryPath != "/app/.stackkit/bin/stackkit" {
		return Release{}, fmt.Errorf("unsupported managed runtime pin")
	}
	manifestRaw, err := readBoundedRegularFile(filepath.Join(dir, "release-manifest.json"), guardrelease.MaxManifestBytes)
	if err != nil {
		return Release{}, err
	}
	manifest, err := guardrelease.ParseManifest(manifestRaw)
	if err != nil {
		return Release{}, err
	}
	if manifest.StackKits == nil {
		return Release{}, fmt.Errorf("signed StackKits identity is absent")
	}
	runtime, ok := manifest.StackKits.ForArch("amd64")
	if !ok || pin.Version != manifest.StackKits.Version || pin.ArchiveSHA256 != runtime.ArchiveSHA256 || pin.BinarySHA256 != runtime.BinarySHA256 {
		return Release{}, fmt.Errorf("managed runtime pin differs from the signed release")
	}
	currentRaw, err := readBoundedRegularFile(filepath.Join(dir, "current-release-manifest.json"), guardrelease.MaxManifestBytes)
	if err != nil {
		return Release{}, err
	}
	currentBundle, err := readBoundedRegularFile(filepath.Join(dir, "current-release-manifest.sigstore.json"), guardrelease.MaxBundleBytes)
	if err != nil {
		return Release{}, err
	}
	initial, err := guardrelease.ParseManifest(currentRaw)
	if err != nil {
		return Release{}, err
	}
	// Authenticate the image-baked policy at issuance even after it has expired;
	// the newer in-process policy, if any, was verified by the refresher.
	verifier, err := guardrelease.NewConfiguredVerifier(func() time.Time { return initial.IssuedAt })
	if err != nil {
		return Release{}, err
	}
	current, err := verifier.VerifyManifest(currentRaw, currentBundle)
	if err != nil {
		return Release{}, err
	}
	if observed := guardrelease.LatestObservedPolicy(); observed != nil && observed.Version == current.Version && observed.IssuedAt.After(current.IssuedAt) {
		current = observed
	}
	if err := current.Fresh(time.Now().UTC()); err != nil {
		return Release{}, err
	}
	if current.Version != manifest.Version || current.StackKits == nil || current.StackKits.Version != pin.Version {
		return Release{}, fmt.Errorf("current Guard policy differs from managed runtime identity")
	}
	currentRuntime, ok := current.StackKits.ForArch("amd64")
	if !ok || currentRuntime.ArchiveSHA256 != runtime.ArchiveSHA256 || currentRuntime.BinarySHA256 != runtime.BinarySHA256 || currentRuntime.BundleSHA256 != runtime.BundleSHA256 {
		return Release{}, fmt.Errorf("current Guard policy changes the managed runtime")
	}
	if denial := current.Admit(current.Version, currentRuntime.ArchiveSHA256); denial != nil {
		return Release{}, denial
	}
	// The extracted pin names the inner StackKits archive. The control plane
	// distributes a different outer runtime bundle whose SHA is anchored by the
	// image's immutable artifact index. Revocation must check that outer SHA.
	if err := admitPinnedOuterRuntimeArchive(manifestRaw, current); err != nil {
		return Release{}, err
	}
	for _, artifact := range current.Artifacts {
		if denial := current.Admit(current.Version, artifact.SHA256); denial != nil {
			return Release{}, denial
		}
	}
	catalog, err := readBoundedRegularFile(filepath.Join(dir, AdvancedOperationsFileName), maxAdvancedOperationsBytes)
	if err != nil {
		return Release{}, err
	}
	if _, err := DecodeAdvancedOperations(catalog, pin.Version); err != nil {
		return Release{}, err
	}
	return Release{receiptPath: pinPath, advancedOperations: catalog, receipt: Receipt{
		SchemaVersion: PinSchemaVersion, Kit: pin.Kit, Version: pin.Version,
		Channel: channelForVersion(pin.Version), Platform: pin.Platform,
		ArchiveSHA256: pin.ArchiveSHA256, IndexSHA256: pin.IndexSHA256,
		// The pin's binary digest is checked above; the executable is held only
		// by the managed node and never exposed from this metadata release.
		VerifiedAt: manifest.IssuedAt,
	}}, nil
}

// admitPinnedOuterRuntimeArchive binds managed-target admission to the same
// image-pinned catalog that the artifact route uses. The catalog's manifest
// digest anchors the seed copied into this target metadata directory.
func admitPinnedOuterRuntimeArchive(seedRaw []byte, current *guardrelease.Manifest) error {
	indexPath := strings.TrimSpace(os.Getenv("TECHSTACK_ARTIFACT_INDEX_PATH"))
	want := strings.TrimSpace(os.Getenv("TECHSTACK_ARTIFACT_INDEX_SHA256"))
	if digestPath := strings.TrimSpace(os.Getenv("TECHSTACK_ARTIFACT_INDEX_SHA256_FILE")); digestPath != "" {
		bound, err := readBoundedRegularFile(digestPath, 65)
		if err != nil {
			return fmt.Errorf("managed runtime index digest: %w", err)
		}
		fromImage := strings.TrimSpace(string(bound))
		if !runtimeIndexDigest(fromImage) || (want != "" && want != fromImage) {
			return fmt.Errorf("managed runtime index digest differs from image")
		}
		want = fromImage
	}
	if indexPath == "" || !runtimeIndexDigest(want) {
		return fmt.Errorf("managed runtime index configuration is unavailable")
	}
	raw, err := readBoundedRegularFile(indexPath, 16<<10)
	if err != nil {
		return fmt.Errorf("read managed runtime index: %w", err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != want {
		return fmt.Errorf("managed runtime index digest mismatch")
	}
	var index struct {
		Schema         string `json:"schema"`
		ReleaseVersion string `json:"release_version"`
		ManifestSHA256 string `json:"manifest_sha256"`
		Artifacts      []struct {
			Kind     string `json:"kind"`
			OS       string `json:"os"`
			Arch     string `json:"arch"`
			SHA256   string `json:"sha256"`
			Size     int64  `json:"size"`
			Filename string `json:"filename"`
		} `json:"artifacts"`
	}
	if err := decodeStrict(raw, &index); err != nil || index.Schema != "techstack.artifact-index/v1" ||
		index.ReleaseVersion != current.Version || index.ManifestSHA256 != fmt.Sprintf("%x", sha256.Sum256(seedRaw)) ||
		len(index.Artifacts) == 0 || len(index.Artifacts) > 4 {
		return fmt.Errorf("managed runtime index does not match the signed release seed")
	}
	outer := ""
	for _, entry := range index.Artifacts {
		if entry.Kind == "stackkit_runtime" && entry.OS == "linux" && entry.Arch == "amd64" {
			if outer != "" || entry.Filename != "stackkit-runtime-linux-amd64.tar.gz" || entry.Size <= 0 || !runtimeIndexDigest(entry.SHA256) {
				return fmt.Errorf("managed runtime index has no unique bounded outer archive")
			}
			outer = entry.SHA256
		}
	}
	if outer == "" {
		return fmt.Errorf("managed runtime index has no linux/amd64 outer archive")
	}
	if denial := current.Admit(current.Version, outer); denial != nil {
		return denial
	}
	return nil
}

func runtimeIndexDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}
