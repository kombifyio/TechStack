// Package updateclient implements the consumer half of the Kombify signed
// auto-update contract (NATIVE-CLIENT-PLATFORM-STANDARD.md section 7): fetch
// a per-channel [manifest.Manifest], verify its signature against embedded
// keys, enforce version monotonicity and the minimum-supported-version
// staleness guard, download the artifact, verify its digest and size, and
// stage it for the platform shell to apply.
//
// Applying (swapping binaries, rollback markers at the install location) is
// deliberately NOT in this package: the platform adapter owns
// apply-at-startup per the thin-shell rule. This package produces a fully
// verified staging directory plus a stage descriptor the shell consumes.
package updateclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/internal/semverlite"
	"github.com/kombifyio/techstack/internal/gocommon/updateclient/manifest"
)

// Errors returned by this package.
var (
	ErrOptedOut        = errors.New("updateclient: auto-update is disabled")
	ErrChannelMismatch = errors.New("updateclient: manifest channel mismatch")
	ErrDowngrade       = errors.New("updateclient: manifest version is not newer than the installed version")
	ErrStaleChannel    = errors.New("updateclient: channel is below the minimum supported version")
	ErrFetchFailed     = errors.New("updateclient: manifest fetch failed")
	ErrDownloadFailed  = errors.New("updateclient: artifact download failed")
	ErrDigestMismatch  = errors.New("updateclient: artifact digest or size mismatch")
)

// Config describes one installed client's update posture.
type Config struct {
	// ManifestURL is the per-channel manifest location (HTTPS).
	ManifestURL string
	// Channel the installation is bound to (edition-bound, e.g. "stable").
	Channel string
	// CurrentVersion is the installed SemVer version.
	CurrentVersion string
	// MinSupportedVersion refuses stale channels when set (SemVer).
	MinSupportedVersion string
	// Keys are the public keys embedded in the binary.
	Keys manifest.Keyring
	// StagingDir is where verified artifacts are staged
	// (<StagingDir>/<version>/...).
	StagingDir string
	// OptOut disables update checks entirely (selfhost-oss sovereignty
	// opt-out). The channel still exists; manual updates stay possible.
	OptOut bool
	// HTTPClient defaults to a 5-minute timeout client (artifact downloads).
	HTTPClient *http.Client
}

// Decision is the outcome of a Check.
type Decision struct {
	// UpdateAvailable is true when a verified, newer manifest exists.
	UpdateAvailable bool
	// Manifest is set when UpdateAvailable is true.
	Manifest *manifest.Manifest
}

// StageDescriptorName is the file written next to the staged artifact; the
// platform shell reads it during apply-at-startup.
const StageDescriptorName = "stage.json"

// StageDescriptor records what was staged and verified.
type StageDescriptor struct {
	Version       string    `json:"version"`
	Channel       string    `json:"channel"`
	PackagePath   string    `json:"package_path"`
	PackageSHA256 string    `json:"package_sha256"`
	PackageSize   int64     `json:"package_size"`
	StagedAt      time.Time `json:"staged_at"`
	KeyID         string    `json:"key_id"`
}

// Client checks for and stages updates.
type Client struct {
	cfg Config
}

// New validates the configuration and returns a Client.
func New(cfg Config) (*Client, error) {
	if cfg.ManifestURL == "" || cfg.Channel == "" || cfg.CurrentVersion == "" || cfg.StagingDir == "" {
		return nil, errors.New("updateclient: ManifestURL, Channel, CurrentVersion, and StagingDir are required")
	}
	if _, err := semverlite.Parse(cfg.CurrentVersion); err != nil {
		return nil, fmt.Errorf("updateclient: invalid CurrentVersion: %w", err)
	}
	if cfg.MinSupportedVersion != "" {
		if _, err := semverlite.Parse(cfg.MinSupportedVersion); err != nil {
			return nil, fmt.Errorf("updateclient: invalid MinSupportedVersion: %w", err)
		}
	}
	return &Client{cfg: cfg}, nil
}

func (c *Client) httpClient() *http.Client {
	if c.cfg.HTTPClient != nil {
		return c.cfg.HTTPClient
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

// Check fetches, verifies, and evaluates the channel manifest. Failure modes
// are fail-closed: an unverifiable or non-monotonic manifest is an error,
// never an update. Callers treat every error as "no update this round" for
// startup purposes — update unavailability never blocks start.
func (c *Client) Check(ctx context.Context) (*Decision, error) {
	if c.cfg.OptOut {
		return nil, ErrOptedOut
	}
	m, err := c.fetchManifest(ctx)
	if err != nil {
		return nil, err
	}
	if verifyErr := m.VerifySignature(c.cfg.Keys); verifyErr != nil {
		return nil, verifyErr
	}
	if m.Channel != c.cfg.Channel {
		return nil, fmt.Errorf("%w: manifest %q, installation %q", ErrChannelMismatch, m.Channel, c.cfg.Channel)
	}
	if c.cfg.MinSupportedVersion != "" {
		minCmp, minErr := semverlite.CompareStrings(m.Version, c.cfg.MinSupportedVersion)
		if minErr != nil {
			return nil, minErr
		}
		if minCmp < 0 {
			return nil, fmt.Errorf("%w: manifest %s < minimum %s", ErrStaleChannel, m.Version, c.cfg.MinSupportedVersion)
		}
	}
	cmp, err := semverlite.CompareStrings(m.Version, c.cfg.CurrentVersion)
	if err != nil {
		return nil, err
	}
	if cmp < 0 {
		// No silent downgrade: a channel serving an older version is refused.
		return nil, fmt.Errorf("%w: manifest %s, installed %s", ErrDowngrade, m.Version, c.cfg.CurrentVersion)
	}
	if cmp == 0 {
		return &Decision{UpdateAvailable: false}, nil
	}
	return &Decision{UpdateAvailable: true, Manifest: m}, nil
}

func (c *Client) fetchManifest(ctx context.Context) (*manifest.Manifest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.ManifestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFetchFailed, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFetchFailed, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: status %d", ErrFetchFailed, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFetchFailed, err)
	}
	return manifest.Parse(raw)
}

// Download fetches the manifest's artifact into the staging directory,
// verifies size and SHA-256 before the descriptor is written, and returns the
// stage descriptor path. A verification failure removes the partial download.
func (c *Client) Download(ctx context.Context, m *manifest.Manifest) (string, error) {
	stageDir := filepath.Join(c.cfg.StagingDir, m.Version)
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		return "", fmt.Errorf("%w: %v", ErrDownloadFailed, err)
	}
	packagePath := filepath.Join(stageDir, "package")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.PackageURL, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDownloadFailed, err)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDownloadFailed, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("%w: status %d", ErrDownloadFailed, resp.StatusCode)
	}

	file, err := os.OpenFile(packagePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDownloadFailed, err)
	}
	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, digest), io.LimitReader(resp.Body, m.PackageSize+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(packagePath)
		return "", fmt.Errorf("%w: %v", ErrDownloadFailed, errors.Join(copyErr, closeErr))
	}
	if written != m.PackageSize || hex.EncodeToString(digest.Sum(nil)) != m.PackageSHA256 {
		_ = os.Remove(packagePath)
		return "", fmt.Errorf("%w: got %d bytes", ErrDigestMismatch, written)
	}

	descriptor := StageDescriptor{
		Version:       m.Version,
		Channel:       m.Channel,
		PackagePath:   packagePath,
		PackageSHA256: m.PackageSHA256,
		PackageSize:   m.PackageSize,
		StagedAt:      time.Now().UTC(),
		KeyID:         m.Signature.KeyID,
	}
	encoded, err := json.MarshalIndent(descriptor, "", "  ")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDownloadFailed, err)
	}
	descriptorPath := filepath.Join(stageDir, StageDescriptorName)
	if err := os.WriteFile(descriptorPath, encoded, 0o600); err != nil {
		return "", fmt.Errorf("%w: %v", ErrDownloadFailed, err)
	}
	return descriptorPath, nil
}
