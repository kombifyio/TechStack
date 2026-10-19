package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/updateclient"
	"github.com/kombifyio/techstack/internal/gocommon/updateclient/manifest"
	"golang.org/x/mod/semver"
)

// `techstack client-update` is the Go half of the Windows client update
// consumer (DESKTOP-FOUNDATION-STANDARD.md §5.7). It fetches the channel
// manifest, verifies its signature against the key embedded below, refuses
// downgrades and channels below the supported floor, downloads the package,
// checks its size and SHA-256, and stages it with a descriptor. It never
// swaps files: the Windows shell applies a staged package at its next start,
// before it spawns the runtime.

// clientUpdateKeyID and clientUpdatePublicKeyPEM identify the production
// update-signing key (DESKTOP-FOUNDATION-STANDARD.md §6.4). Only
// manifests signed by an embedded key are accepted; there is no runtime
// override, because a user-writable trust setting would let any local process
// have the client install its own package.
const clientUpdateKeyID = "kombify-desktop-update-2026-v1"

// The Cloud-only helper selects this at build time. Neither client exposes a
// command-line or configuration switch that can change its installer edition.
var clientUpdateEdition = "local"

const clientUpdatePublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAOLkR9JyjFiPmnm11tQ1f2ghRR7tMZlxBkSJhP1MxQ2w=
-----END PUBLIC KEY-----
`

type clientUpdateResult struct {
	Status        string `json:"status"`
	Version       string `json:"version,omitempty"`
	Installed     string `json:"installed"`
	Descriptor    string `json:"descriptor,omitempty"`
	Error         string `json:"error,omitempty"`
	PackagePath   string `json:"package_path,omitempty"`
	PackageSHA256 string `json:"package_sha256,omitempty"`
	PackageSize   int64  `json:"package_size,omitempty"`
}

func isClientUpdateMode(args []string) bool {
	return len(args) > 1 && strings.TrimSpace(args[1]) == "client-update"
}

func clientUpdateKeyring() (manifest.Keyring, error) {
	key, err := manifest.ParseEd25519PublicKeyPEM([]byte(clientUpdatePublicKeyPEM))
	if err != nil {
		return manifest.Keyring{}, fmt.Errorf("embedded update key: %w", err)
	}
	return manifest.Keyring{Ed25519: map[string]ed25519.PublicKey{clientUpdateKeyID: key}}, nil
}

// runClientUpdateMode prints one JSON result line and exits non-zero on any
// failure. Callers treat every failure as "no update this round".
func runClientUpdateMode(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("techstack client-update", flag.ContinueOnError)
	manifestURL := flags.String("manifest-url", "", "HTTPS URL of the channel's update manifest")
	channel := flags.String("channel", "stable", "update channel bound to this installation")
	stagingDir := flags.String("staging-dir", "", "directory that receives verified packages")
	minSupported := flags.String("min-supported-version", "", "refuse a channel below this version")
	recoveryVersion := flags.String("recovery-version", "", "stage the signed package for exactly this running version, without applying it")
	rollbackVersion := flags.String("rollback-version", "", "stage a signed exact-version rollback package at or below the running version, without applying it")
	verifyVersion := flags.String("verify-staged-version", "", "verify a staged signed package offline before installer handoff")
	verifyRollback := flags.Bool("verify-rollback", false, "verify an already staged rollback package, never download or apply a downgrade")
	timeout := flags.Duration("timeout", 10*time.Minute, "budget for the check and the download")
	if err := flags.Parse(args); err != nil {
		return err
	}

	result := clientUpdateResult{Installed: version}
	var err error
	if *rollbackVersion != "" && (*verifyVersion != "" || *verifyRollback || *recoveryVersion != "") {
		err = errors.New("rollback staging cannot be combined with another update mode")
	} else if *verifyVersion != "" {
		purpose := "update"
		if *verifyRollback {
			purpose = "rollback"
		} else if *recoveryVersion != "" {
			if *recoveryVersion != version {
				return errors.New("recovery version must equal the running client version")
			}
			purpose = "recovery"
		}
		keys, keyErr := clientUpdateKeyring()
		if keyErr != nil {
			err = keyErr
		} else {
			if purpose == "update" && *minSupported != "" && (!semver.IsValid("v"+*minSupported) || semver.Compare("v"+*verifyVersion, "v"+*minSupported) < 0) {
				err = errors.New("staged update is below the minimum supported version")
			} else {
				err = verifyStagedClientPackage(*stagingDir, *verifyVersion, *channel, version, purpose, keys, &result)
			}
		}
	} else if *verifyRollback {
		err = errors.New("rollback verification requires --verify-staged-version")
	} else if *rollbackVersion != "" {
		err = stageClientRollback(ctx, *manifestURL, *channel, *stagingDir, *rollbackVersion, *timeout, nil, &result)
	} else if *recoveryVersion != "" {
		err = stageInstalledClientRecovery(ctx, *manifestURL, *channel, *stagingDir, *recoveryVersion, *timeout, nil, &result)
	} else {
		err = stageClientUpdate(ctx, *manifestURL, *channel, *stagingDir, *minSupported, *timeout, nil, &result)
	}
	if err != nil {
		result.Status = "error"
		result.Error = err.Error()
	}
	encoded, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return marshalErr
	}
	fmt.Fprintln(os.Stdout, string(encoded))
	return err
}

func stageClientUpdate(ctx context.Context, manifestURL, channel, stagingDir, minSupported string, timeout time.Duration, httpClient *http.Client, result *clientUpdateResult) error {
	if err := validateClientUpdateFeed(manifestURL, channel, ""); err != nil {
		return err
	}
	if !strings.HasPrefix(strings.TrimSpace(manifestURL), "https://") {
		return errors.New("--manifest-url must be an HTTPS URL")
	}
	keys, err := clientUpdateKeyring()
	if err != nil {
		return err
	}
	client, err := updateclient.New(updateclient.Config{
		ManifestURL:         strings.TrimSpace(manifestURL),
		Channel:             strings.TrimSpace(channel),
		CurrentVersion:      version,
		MinSupportedVersion: strings.TrimSpace(minSupported),
		Keys:                keys,
		StagingDir:          strings.TrimSpace(stagingDir),
		HTTPClient:          httpClient,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	decision, err := client.Check(ctx)
	if err != nil {
		return err
	}
	if !decision.UpdateAvailable {
		result.Status = "current"
		return nil
	}
	if err := validateClientUpdatePackage(decision.Manifest); err != nil {
		return err
	}
	descriptor, err := client.Download(ctx, decision.Manifest)
	if err != nil {
		return err
	}
	if err := preserveClientStageManifest(descriptor, decision.Manifest); err != nil {
		return err
	}
	result.Status = "staged"
	result.Version = decision.Manifest.Version
	result.Descriptor = descriptor
	return nil
}

// Recovery stages only the running installation's own signed release. It does
// not relax normal upgrade monotonicity or download an arbitrary older version.
// MSI installations use it when no Burn bundle is available for rollback.
func stageInstalledClientRecovery(ctx context.Context, manifestURL, channel, stagingDir, recoveryVersion string, timeout time.Duration, httpClient *http.Client, result *clientUpdateResult) error {
	if strings.TrimSpace(recoveryVersion) != version {
		return errors.New("recovery version must equal the running client version")
	}
	return stageSignedExactClientPackage(ctx, manifestURL, channel, stagingDir, recoveryVersion, "recovery-staged", timeout, httpClient, result)
}

// A validated pending rollback needs both the new release's uninstaller and
// the prior release's installer. This stages signed packages only; execution
// remains exclusively in the Windows shell's bound, locked handoff.
func stageClientRollback(ctx context.Context, manifestURL, channel, stagingDir, rollbackVersion string, timeout time.Duration, httpClient *http.Client, result *clientUpdateResult) error {
	if !semver.IsValid("v"+rollbackVersion) || semver.Compare("v"+rollbackVersion, "v"+version) > 0 {
		return errors.New("rollback package must be an exact version at or below the running client version")
	}
	return stageSignedExactClientPackage(ctx, manifestURL, channel, stagingDir, rollbackVersion, "rollback-staged", timeout, httpClient, result)
}

func stageSignedExactClientPackage(ctx context.Context, manifestURL, channel, stagingDir, expectedVersion, status string, timeout time.Duration, httpClient *http.Client, result *clientUpdateResult) error {
	if err := validateClientUpdateFeed(manifestURL, channel, expectedVersion); err != nil {
		return err
	}
	if !strings.HasPrefix(strings.TrimSpace(manifestURL), "https://") {
		return errors.New("--manifest-url must be an HTTPS URL")
	}
	keys, err := clientUpdateKeyring()
	if err != nil {
		return err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Minute}
	}
	client, err := updateclient.New(updateclient.Config{
		ManifestURL: strings.TrimSpace(manifestURL), Channel: strings.TrimSpace(channel),
		CurrentVersion: version, Keys: keys, StagingDir: strings.TrimSpace(stagingDir), HTTPClient: httpClient,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(manifestURL), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	response, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("signed recovery manifest: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("signed recovery manifest is unavailable: HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	m, err := manifest.Parse(raw)
	if err != nil {
		return err
	}
	if err := m.VerifySignature(keys); err != nil {
		return err
	}
	if m.Version != expectedVersion || m.Channel != strings.TrimSpace(channel) {
		return errors.New("signed package manifest does not match the requested exact version and installed channel")
	}
	if err := validateClientUpdatePackage(m); err != nil {
		return err
	}
	descriptor, err := client.Download(ctx, m)
	if err != nil {
		return err
	}
	if err := preserveClientStageManifest(descriptor, m); err != nil {
		return err
	}
	result.Status = status
	result.Version = m.Version
	result.Descriptor = descriptor
	return nil
}

func preserveClientStageManifest(descriptor string, m *manifest.Manifest) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	directory := filepath.Dir(descriptor)
	temporary, err := os.CreateTemp(directory, "manifest-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.Write(raw); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(directory, "manifest.json"))
}

// This helper accepts keys for isolated module probes. The CLI always supplies
// the embedded keyring, never a caller-provided trust override. stage.json is
// only a discovery hint; all installer authority comes from the signed manifest.
func verifyStagedClientPackage(root, expectedVersion, channel, installedVersion, purpose string, keys manifest.Keyring, result *clientUpdateResult) error {
	if !semver.IsValid("v"+expectedVersion) || filepath.Base(expectedVersion) != expectedVersion ||
		strings.ContainsAny(expectedVersion, `/\\`) {
		return errors.New("invalid staged package version")
	}
	comparison := semver.Compare("v"+expectedVersion, "v"+installedVersion)
	if !semver.IsValid("v"+installedVersion) || (purpose == "update" && comparison <= 0) ||
		(purpose == "recovery" && expectedVersion != installedVersion) || (purpose == "rollback" && comparison > 0) ||
		(purpose != "update" && purpose != "recovery" && purpose != "rollback") {
		return errors.New("staged package version is not admitted for this installation")
	}
	stage := filepath.Join(root, expectedVersion)
	for _, path := range []string{root, stage, filepath.Join(stage, "manifest.json"), filepath.Join(stage, "package")} {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("staged package boundary contains a symbolic link")
		}
	}
	manifestFile, err := os.Open(filepath.Join(stage, "manifest.json"))
	if err != nil {
		return err
	}
	raw, readErr := io.ReadAll(io.LimitReader(manifestFile, (1<<20)+1))
	manifestFile.Close()
	if readErr != nil {
		return readErr
	}
	if len(raw) > 1<<20 {
		return errors.New("staged manifest exceeds the supported size")
	}
	m, err := manifest.Parse(raw)
	if err != nil {
		return err
	}
	if err := m.VerifySignature(keys); err != nil {
		return err
	}
	if m.Version != expectedVersion || m.Channel != channel {
		return errors.New("signed package does not match the requested version and channel")
	}
	if err := validateClientUpdatePackage(m); err != nil {
		return err
	}
	packagePath := filepath.Join(stage, "package")
	file, err := os.Open(packagePath)
	if err != nil {
		return err
	}
	defer file.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, io.LimitReader(file, m.PackageSize+1))
	if err != nil {
		return err
	}
	if size != m.PackageSize || hex.EncodeToString(digest.Sum(nil)) != m.PackageSHA256 {
		return errors.New("staged package does not match its signed digest and size")
	}
	result.Status = "verified-" + purpose
	result.Version = m.Version
	result.PackagePath = packagePath
	result.PackageSHA256 = m.PackageSHA256
	result.PackageSize = m.PackageSize
	return nil
}

func validateClientUpdateFeed(rawURL, channel, exactVersion string) error {
	if clientUpdateEdition != "cloud" {
		return nil
	}
	expected := "https://github.com/kombifyio/TechStack/releases/download/windows-cloud-beta/kombify-techstack-windows-cloud-beta.json"
	if exactVersion != "" {
		expected = "https://github.com/kombifyio/TechStack/releases/download/windows-cloud-v" + exactVersion + "/kombify-techstack-windows-cloud-beta.json"
	}
	if channel != "beta" || rawURL != expected {
		return errors.New("the Cloud updater requires its installed beta feed and exact Cloud recovery release")
	}
	return nil
}

func validateClientUpdatePackage(m *manifest.Manifest) error {
	prefix, installer := "v", "kombify-Techstack-Setup.exe"
	if clientUpdateEdition == "cloud" {
		prefix, installer = "windows-cloud-v", "kombify-Techstack-Cloud-Setup.exe"
		if m.Channel != "beta" {
			return errors.New("the Cloud installer is bound to the beta channel")
		}
	} else if clientUpdateEdition != "local" {
		return errors.New("unsupported Windows installer edition")
	}
	if m.PackageURL != "https://github.com/kombifyio/TechStack/releases/download/"+prefix+m.Version+"/"+installer {
		return errors.New("the signed installer does not belong to this product, edition and exact release")
	}
	return nil
}
