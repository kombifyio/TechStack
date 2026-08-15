// Package stackkitrelease binds Techstack to one exact published StackKits
// artifact. It never reads a StackKits source checkout.
package stackkitrelease

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	PinSchemaVersion                   = "techstack.stackkit-release-pin/v2"
	legacyPinSchemaVersion             = "techstack.stackkit-release-pin/v1"
	ReceiptSchemaVersion               = "stackkit.release-receipt/v1"
	IndexSchemaVersion                 = "stackkits-release-index/v1"
	releaseReceiptName                 = "release-receipt.json"
	releaseIndexName                   = "stackkits-release-index-v1.json"
	releaseIndexAttestationName        = "stackkits-release-index-v1.json.intoto.jsonl"
	trustedRootName                    = "sigstore-trusted-root.jsonl"
	trustedRepository                  = "kombifyio/stackKits"
	githubOIDCIssuer                   = "https://token.actions.githubusercontent.com"
	githubAttestationPredicate         = "https://slsa.dev/provenance/v1"
	spdxJSONMediaType                  = "application/spdx+json"
	inTotoJSONLMediaType               = "application/vnd.in-toto+jsonl"
	sigstoreTrustedRootMediaType       = "application/vnd.dev.sigstore.trustedroot+json;version=0.1"
	maxPinBytes                  int64 = 64 << 10
	maxReceiptBytes              int64 = 64 << 10
	maxIndexBytes                int64 = 4 << 20
	maxTrustBlobBytes            int64 = 64 << 20
	maxReleaseBlobBytes          int64 = 4 << 30
	maxExecutableBytes           int64 = 512 << 20
)

var (
	exactReleasePattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-(beta|edge)\.[0-9]+)?$`)
	safeKitPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	sha256Pattern       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Pin is Techstack's immutable deployment input.
type Pin struct {
	SchemaVersion string   `json:"schemaVersion"`
	Kit           string   `json:"kit"`
	Version       string   `json:"version"`
	Platform      Platform `json:"platform"`
	ArchiveSHA256 string   `json:"archiveSha256"`
	IndexSHA256   string   `json:"indexSha256"`
	BinarySHA256  string   `json:"binarySha256,omitempty"`
	BinaryPath    string   `json:"binaryPath"`
}

type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type Receipt struct {
	SchemaVersion          string    `json:"schemaVersion"`
	Kit                    string    `json:"kit"`
	Version                string    `json:"version"`
	Channel                string    `json:"channel"`
	Platform               Platform  `json:"platform"`
	ArchiveSHA256          string    `json:"archiveSha256"`
	SBOMSHA256             string    `json:"sbomSha256"`
	AttestationSHA256      string    `json:"attestationSha256"`
	AttestationIssuer      string    `json:"attestationIssuer"`
	AttestationSubject     string    `json:"attestationSubject"`
	TrustedRootSHA256      string    `json:"trustedRootSha256"`
	IndexSHA256            string    `json:"indexSha256"`
	IndexAttestationSHA256 string    `json:"indexAttestationSha256"`
	VerifiedAt             time.Time `json:"verifiedAt"`
	InstallDir             string    `json:"installDir"`
}

// Release is the runtime identity consumers need: an exact executable and its
// published artifact identity. Legacy v1 pins retain their cached receipt.
type Release struct {
	binaryPath  string
	installDir  string
	receiptPath string
	receipt     Receipt
}

// Cache resolves current artifact pins directly and legacy v1 pins from the
// StackKits-owned .stackkit/releases directory.
type Cache struct {
	Root string
}

type releaseIndex struct {
	SchemaVersion string            `json:"schemaVersion"`
	Release       releaseDescriptor `json:"release"`
	Assets        []releaseAsset    `json:"assets"`
}

type releaseDescriptor struct {
	Repository  string    `json:"repository"`
	Version     string    `json:"version"`
	PublishedAt time.Time `json:"publishedAt"`
	TrustedRoot blob      `json:"trustedRoot"`
}

type releaseAsset struct {
	Kit         string      `json:"kit"`
	Version     string      `json:"version"`
	Channel     string      `json:"channel"`
	Platform    Platform    `json:"platform"`
	Archive     blob        `json:"archive"`
	SBOM        blob        `json:"sbom"`
	Attestation attestation `json:"attestation"`
}

type blob struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	MediaType string `json:"mediaType"`
}

type attestation struct {
	blob
	Issuer              string `json:"issuer"`
	CertificateIdentity string `json:"certificateIdentity"`
	Subject             string `json:"subject"`
	PredicateType       string `json:"predicateType"`
}

// LoadPin reads one strict, bounded deployment pin.
func LoadPin(path string) (Pin, error) {
	data, err := readBoundedRegularFile(path, maxPinBytes)
	if err != nil {
		return Pin{}, fmt.Errorf("read StackKits release pin: %w", err)
	}
	var pin Pin
	if err := decodeStrict(data, &pin); err != nil {
		return Pin{}, fmt.Errorf("decode StackKits release pin: %w", err)
	}
	if err := pin.validate(); err != nil {
		return Pin{}, err
	}
	return pin, nil
}

// ResolvePin loads and resolves a deployment pin in one fail-closed operation.
func (cache Cache) ResolvePin(path string) (Release, error) {
	pin, err := LoadPin(path)
	if err != nil {
		return Release{}, err
	}
	if pin.SchemaVersion == PinSchemaVersion {
		return resolveArtifactPin(pin, path)
	}
	return cache.Resolve(pin)
}

// Resolve revalidates the receipt, index, cached trust set, release archive,
// and configured executable before returning a usable runtime consumer.
func (cache Cache) Resolve(pin Pin) (Release, error) {
	if err := pin.validate(); err != nil {
		return Release{}, err
	}
	if pin.SchemaVersion == PinSchemaVersion {
		return resolveArtifactPin(pin, "")
	}
	root, installDir, err := cache.releaseDir(pin)
	if err != nil {
		return Release{}, err
	}
	if err := requireSecureDirectoryChain(root, installDir); err != nil {
		return Release{}, err
	}

	receiptPath := filepath.Join(installDir, releaseReceiptName)
	receiptData, err := readBoundedRegularFile(receiptPath, maxReceiptBytes)
	if err != nil {
		return Release{}, fmt.Errorf("read cached StackKits release receipt: %w", err)
	}
	var receipt Receipt
	if err := decodeStrict(receiptData, &receipt); err != nil {
		return Release{}, fmt.Errorf("decode cached StackKits release receipt: %w", err)
	}
	if err := validateReceipt(receipt, pin, installDir); err != nil {
		return Release{}, err
	}

	indexPath := filepath.Join(installDir, releaseIndexName)
	indexData, err := readBoundedRegularFile(indexPath, maxIndexBytes)
	if err != nil {
		return Release{}, fmt.Errorf("read cached StackKits release index: %w", err)
	}
	if digestBytes(indexData) != pin.IndexSHA256 {
		return Release{}, fmt.Errorf("cached StackKits release index does not match pinned SHA-256")
	}
	var index releaseIndex
	if err := decodeStrict(indexData, &index); err != nil {
		return Release{}, fmt.Errorf("decode cached StackKits release index: %w", err)
	}
	asset, err := validateIndex(index, pin, receipt)
	if err != nil {
		return Release{}, err
	}

	if err := verifyCachedBlob(installDir, asset.Archive, pin.ArchiveSHA256, maxReleaseBlobBytes); err != nil {
		return Release{}, err
	}
	if err := verifyCachedBlob(installDir, asset.SBOM, receipt.SBOMSHA256, maxReleaseBlobBytes); err != nil {
		return Release{}, err
	}
	if err := verifyCachedBlob(installDir, asset.Attestation.blob, receipt.AttestationSHA256, maxTrustBlobBytes); err != nil {
		return Release{}, err
	}
	if err := verifyCachedBlob(installDir, index.Release.TrustedRoot, receipt.TrustedRootSHA256, maxTrustBlobBytes); err != nil {
		return Release{}, err
	}
	if err := verifyNamedBlob(
		filepath.Join(installDir, releaseIndexAttestationName),
		receipt.IndexAttestationSHA256,
		maxTrustBlobBytes,
	); err != nil {
		return Release{}, fmt.Errorf("verify cached release-index attestation: %w", err)
	}

	binaryPath, err := filepath.Abs(filepath.Clean(pin.BinaryPath))
	if err != nil {
		return Release{}, fmt.Errorf("resolve pinned StackKits binary: %w", err)
	}
	current, err := digestRegularFile(binaryPath, maxExecutableBytes)
	if err != nil {
		return Release{}, fmt.Errorf("verify pinned StackKits binary: %w", err)
	}
	published, err := digestArchiveExecutable(
		filepath.Join(installDir, asset.Archive.Name),
		asset.Archive.Name,
		pin.Platform,
	)
	if err != nil {
		return Release{}, fmt.Errorf("verify canonical executable in published StackKits archive: %w", err)
	}
	if current != published {
		return Release{}, fmt.Errorf("configured StackKits binary differs from the pinned published release")
	}

	return Release{
		binaryPath:  binaryPath,
		installDir:  installDir,
		receiptPath: receiptPath,
		receipt:     receipt,
	}, nil
}

func (release Release) BinaryPath() string {
	return release.binaryPath
}

func (release Release) InstallDir() string {
	return release.installDir
}

func (release Release) ReceiptPath() string {
	return release.receiptPath
}

func (release Release) Receipt() Receipt {
	return release.receipt
}

func resolveArtifactPin(pin Pin, pinPath string) (Release, error) {
	binaryPath, err := filepath.Abs(filepath.Clean(pin.BinaryPath))
	if err != nil {
		return Release{}, fmt.Errorf("resolve pinned StackKits binary: %w", err)
	}
	current, err := digestRegularFile(binaryPath, maxExecutableBytes)
	if err != nil {
		return Release{}, fmt.Errorf("verify pinned StackKits binary: %w", err)
	}
	if current.sha256 != pin.BinarySHA256 {
		return Release{}, fmt.Errorf("configured StackKits binary differs from the immutable artifact pin")
	}
	return Release{
		binaryPath:  binaryPath,
		installDir:  filepath.Dir(binaryPath),
		receiptPath: pinPath,
		receipt: Receipt{
			SchemaVersion: PinSchemaVersion,
			Kit:           pin.Kit,
			Version:       pin.Version,
			Channel:       channelForVersion(pin.Version),
			Platform:      pin.Platform,
			ArchiveSHA256: pin.ArchiveSHA256,
			IndexSHA256:   pin.IndexSHA256,
		},
	}, nil
}

func (pin Pin) validate() error {
	return pin.validateForPlatform(Platform{OS: runtime.GOOS, Arch: runtime.GOARCH})
}

func (pin Pin) validateForPlatform(expected Platform) error {
	if pin.SchemaVersion != PinSchemaVersion && pin.SchemaVersion != legacyPinSchemaVersion {
		return fmt.Errorf("unsupported StackKits release pin schema %q", pin.SchemaVersion)
	}
	if !safeKitPattern.MatchString(pin.Kit) {
		return fmt.Errorf("invalid pinned StackKit %q", pin.Kit)
	}
	if !exactReleasePattern.MatchString(pin.Version) {
		return fmt.Errorf("StackKits release version %q is not an exact stable, beta, or edge tag", pin.Version)
	}
	if pin.Platform != expected {
		return fmt.Errorf(
			"pinned StackKits platform %s/%s does not match expected runtime %s/%s",
			pin.Platform.OS,
			pin.Platform.Arch,
			expected.OS,
			expected.Arch,
		)
	}
	if !sha256Pattern.MatchString(pin.ArchiveSHA256) {
		return fmt.Errorf("pinned StackKits archive SHA-256 is invalid")
	}
	if !sha256Pattern.MatchString(pin.IndexSHA256) {
		return fmt.Errorf("pinned StackKits release-index SHA-256 is invalid")
	}
	if pin.SchemaVersion == PinSchemaVersion && !sha256Pattern.MatchString(pin.BinarySHA256) {
		return fmt.Errorf("pinned StackKits binary SHA-256 is invalid")
	}
	if strings.TrimSpace(pin.BinaryPath) == "" {
		return fmt.Errorf("pinned StackKits binary path is required")
	}
	return nil
}

func (cache Cache) releaseDir(pin Pin) (string, string, error) {
	if strings.TrimSpace(cache.Root) == "" {
		return "", "", fmt.Errorf("StackKits release cache root is required")
	}
	root, err := filepath.Abs(filepath.Clean(cache.Root))
	if err != nil {
		return "", "", fmt.Errorf("resolve StackKits release cache root: %w", err)
	}
	installDir := filepath.Join(
		root,
		pin.Kit,
		pin.Version,
		pin.Platform.OS+"-"+pin.Platform.Arch,
	)
	relative, err := filepath.Rel(root, installDir)
	if err != nil || relative == "." || relative == ".." ||
		filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("StackKits release path escapes the configured cache")
	}
	return root, installDir, nil
}

func validateReceipt(receipt Receipt, pin Pin, installDir string) error {
	if receipt.SchemaVersion != ReceiptSchemaVersion {
		return fmt.Errorf("unsupported StackKits release receipt schema %q", receipt.SchemaVersion)
	}
	if receipt.Kit != pin.Kit ||
		receipt.Version != pin.Version ||
		receipt.Platform != pin.Platform {
		return fmt.Errorf("StackKits release receipt does not match the exact deployment pin")
	}
	if receipt.Channel != channelForVersion(pin.Version) {
		return fmt.Errorf("StackKits release receipt channel %q does not match %s", receipt.Channel, pin.Version)
	}
	for label, value := range map[string]string{
		"archive":           receipt.ArchiveSHA256,
		"SBOM":              receipt.SBOMSHA256,
		"attestation":       receipt.AttestationSHA256,
		"trusted root":      receipt.TrustedRootSHA256,
		"release index":     receipt.IndexSHA256,
		"index attestation": receipt.IndexAttestationSHA256,
	} {
		if !sha256Pattern.MatchString(value) {
			return fmt.Errorf("StackKits release receipt %s SHA-256 is invalid", label)
		}
	}
	if receipt.ArchiveSHA256 != pin.ArchiveSHA256 ||
		receipt.IndexSHA256 != pin.IndexSHA256 {
		return fmt.Errorf("StackKits release receipt does not match the immutable deployment digests")
	}
	if receipt.AttestationIssuer != githubOIDCIssuer {
		return fmt.Errorf("StackKits release receipt issuer %q is not trusted", receipt.AttestationIssuer)
	}
	if strings.TrimSpace(receipt.AttestationSubject) == "" {
		return fmt.Errorf("StackKits release receipt attestation subject is required")
	}
	if receipt.VerifiedAt.IsZero() {
		return fmt.Errorf("StackKits release receipt verifiedAt is required")
	}
	receiptDir, err := filepath.Abs(filepath.Clean(receipt.InstallDir))
	if err != nil || receiptDir != installDir {
		return fmt.Errorf("StackKits release receipt installDir does not bind the selected cache")
	}
	return nil
}

func validateIndex(index releaseIndex, pin Pin, receipt Receipt) (releaseAsset, error) {
	if index.SchemaVersion != IndexSchemaVersion {
		return releaseAsset{}, fmt.Errorf("unsupported StackKits release index schema %q", index.SchemaVersion)
	}
	if index.Release.Repository != trustedRepository {
		return releaseAsset{}, fmt.Errorf("StackKits release repository %q is not trusted", index.Release.Repository)
	}
	if index.Release.Version != pin.Version || index.Release.PublishedAt.IsZero() {
		return releaseAsset{}, fmt.Errorf("StackKits release index does not describe published release %s", pin.Version)
	}
	if err := validateBlob(index.Release.TrustedRoot, trustedRootName); err != nil {
		return releaseAsset{}, fmt.Errorf("invalid StackKits trusted root: %w", err)
	}
	if index.Release.TrustedRoot.MediaType != sigstoreTrustedRootMediaType {
		return releaseAsset{}, fmt.Errorf("StackKits trusted-root media type is unsupported")
	}

	var selected *releaseAsset
	for i := range index.Assets {
		asset := &index.Assets[i]
		if asset.Kit != pin.Kit || asset.Version != pin.Version || asset.Platform != pin.Platform {
			continue
		}
		if selected != nil {
			return releaseAsset{}, fmt.Errorf("StackKits release index contains duplicate pinned assets")
		}
		selected = asset
	}
	if selected == nil {
		return releaseAsset{}, fmt.Errorf("StackKits release index has no asset for the exact deployment pin")
	}
	asset := *selected
	if asset.Channel != receipt.Channel {
		return releaseAsset{}, fmt.Errorf("StackKits release-index channel does not match the verified receipt")
	}
	if err := validateBlob(asset.Archive, ""); err != nil {
		return releaseAsset{}, fmt.Errorf("invalid StackKits archive: %w", err)
	}
	if err := validateBlob(asset.SBOM, ""); err != nil {
		return releaseAsset{}, fmt.Errorf("invalid StackKits SBOM: %w", err)
	}
	if asset.SBOM.MediaType != spdxJSONMediaType {
		return releaseAsset{}, fmt.Errorf("StackKits SBOM media type is unsupported")
	}
	if err := validateBlob(asset.Attestation.blob, ""); err != nil {
		return releaseAsset{}, fmt.Errorf("invalid StackKits attestation: %w", err)
	}
	if asset.Attestation.MediaType != inTotoJSONLMediaType ||
		asset.Attestation.Issuer != githubOIDCIssuer ||
		asset.Attestation.PredicateType != githubAttestationPredicate {
		return releaseAsset{}, fmt.Errorf("StackKits release attestation policy is unsupported")
	}
	expectedIdentity := "https://github.com/kombifyio/StackKits/.github/workflows/release.yml@refs/tags/" + pin.Version
	if asset.Attestation.CertificateIdentity != expectedIdentity {
		return releaseAsset{}, fmt.Errorf("StackKits release attestation identity is not trusted")
	}
	if asset.Attestation.Subject != asset.Archive.Name ||
		asset.Attestation.Subject != receipt.AttestationSubject ||
		asset.Attestation.Issuer != receipt.AttestationIssuer {
		return releaseAsset{}, fmt.Errorf("StackKits release attestation does not match its archive and receipt")
	}
	if asset.Archive.SHA256 != pin.ArchiveSHA256 ||
		asset.SBOM.SHA256 != receipt.SBOMSHA256 ||
		asset.Attestation.SHA256 != receipt.AttestationSHA256 ||
		index.Release.TrustedRoot.SHA256 != receipt.TrustedRootSHA256 {
		return releaseAsset{}, fmt.Errorf("StackKits release index does not match the pinned receipt digests")
	}
	return asset, nil
}

func validateBlob(value blob, exactName string) error {
	if strings.TrimSpace(value.Name) == "" ||
		filepath.Base(value.Name) != value.Name ||
		value.Name == "." {
		return fmt.Errorf("unsafe blob name %q", value.Name)
	}
	if exactName != "" && value.Name != exactName {
		return fmt.Errorf("blob name %q must be %q", value.Name, exactName)
	}
	if strings.TrimSpace(value.URL) == "" {
		return fmt.Errorf("blob URL is required")
	}
	if !sha256Pattern.MatchString(value.SHA256) {
		return fmt.Errorf("blob SHA-256 is invalid")
	}
	if strings.TrimSpace(value.MediaType) == "" {
		return fmt.Errorf("blob media type is required")
	}
	return nil
}

func verifyCachedBlob(dir string, value blob, expected string, limit int64) error {
	if err := validateBlob(value, ""); err != nil {
		return err
	}
	if value.SHA256 != expected {
		return fmt.Errorf("cached release blob %s does not match the pinned receipt", value.Name)
	}
	if err := verifyNamedBlob(filepath.Join(dir, value.Name), expected, limit); err != nil {
		return fmt.Errorf("verify cached release blob %s: %w", value.Name, err)
	}
	return nil
}

func verifyNamedBlob(path, expected string, limit int64) error {
	actual, err := digestRegularFile(path, limit)
	if err != nil {
		return err
	}
	if actual.sha256 != expected {
		return fmt.Errorf("SHA-256 mismatch: expected %s, got %s", expected, actual.sha256)
	}
	return nil
}

func requireSecureDirectoryChain(root, leaf string) error {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect StackKits release cache root: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("StackKits release cache root must be a non-symlink directory")
	}
	relative, err := filepath.Rel(root, leaf)
	if err != nil {
		return fmt.Errorf("resolve StackKits release cache path: %w", err)
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return fmt.Errorf("inspect StackKits release cache directory %s: %w", current, statErr)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("StackKits release cache directory %s must not be a symlink", current)
		}
	}
	return nil
}

func readBoundedRegularFile(path string, limit int64) ([]byte, error) {
	file, info, err := openRegularFile(path, limit)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, info.Size()+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != info.Size() {
		return nil, fmt.Errorf("%s changed or was truncated while read", filepath.Base(path))
	}
	if err := ensureUnchanged(file, info); err != nil {
		return nil, err
	}
	return data, nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return fmt.Errorf("trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode trailing JSON: %w", err)
	}
	return nil
}

type fileDigest struct {
	sha256 string
	size   int64
}

func digestRegularFile(path string, limit int64) (fileDigest, error) {
	file, info, err := openRegularFile(path, limit)
	if err != nil {
		return fileDigest{}, err
	}
	defer file.Close()
	digest := sha256.New()
	written, err := io.Copy(digest, io.LimitReader(file, info.Size()+1))
	if err != nil {
		return fileDigest{}, err
	}
	if written != info.Size() {
		return fileDigest{}, fmt.Errorf("%s changed or was truncated while read", filepath.Base(path))
	}
	if err := ensureUnchanged(file, info); err != nil {
		return fileDigest{}, err
	}
	return fileDigest{sha256: hex.EncodeToString(digest.Sum(nil)), size: written}, nil
}

func openRegularFile(path string, limit int64) (*os.File, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() <= 0 || info.Size() > limit {
		return nil, nil, fmt.Errorf("%s must be a bounded regular non-symlink file", filepath.Base(path))
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Size() != info.Size() {
		_ = file.Close()
		return nil, nil, fmt.Errorf("%s changed before it was read", filepath.Base(path))
	}
	return file, opened, nil
}

func ensureUnchanged(file *os.File, before os.FileInfo) error {
	after, err := file.Stat()
	if err != nil || after.Size() != before.Size() ||
		!after.ModTime().Equal(before.ModTime()) {
		return fmt.Errorf("%s changed while it was read", before.Name())
	}
	return nil
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func channelForVersion(version string) string {
	switch {
	case strings.Contains(version, "-beta."):
		return "beta"
	case strings.Contains(version, "-edge."):
		return "edge"
	default:
		return "stable"
	}
}
