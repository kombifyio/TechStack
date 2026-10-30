package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/fulllocal"
	"github.com/kombifyio/techstack/pkg/guardrelease"
	"github.com/kombifyio/techstack/pkg/logger"
)

const (
	guardReleaseSeedManifestFile = "release-manifest-seed.json"
	guardReleaseSeedManifestSig  = "release-manifest-seed.sigstore.json"
	// guardReleaseRefreshEnv set to "off" disables the manifest refresher.
	guardReleaseRefreshEnv = "TECHSTACK_GUARD_RELEASE_REFRESH"
	// guardReleaseSourceEnv names the GitHub repository (owner/name) whose
	// guard-v<version> release the refresher reads.
	guardReleaseSourceEnv = "TECHSTACK_GUARD_RELEASE_SOURCE"
	// guardReleaseRefreshIntervalEnv is how often the refresher looks, default 1h.
	guardReleaseRefreshIntervalEnv = "TECHSTACK_GUARD_RELEASE_REFRESH_INTERVAL"

	defaultGuardReleaseSource   = "kombifyio/TechStack"
	defaultGuardRefreshInterval = time.Hour
	minGuardRefreshInterval     = time.Minute
	guardManifestAgeAlarm       = 26 * time.Hour
	guardReleaseRefreshTimeout  = 45 * time.Second
	githubAPIBase               = "https://api.github.com"
)

// guardManifestVerifier authenticates a release manifest. The production
// implementation is the same guardrelease.Verifier, with the same pinned signer
// identity and trusted root, that every Guard applies.
type guardManifestVerifier interface {
	VerifyManifest(raw, bundleJSON []byte) (*guardrelease.Manifest, error)
}

var guardManifestVerifierFactory = func(now func() time.Time) (guardManifestVerifier, error) {
	return guardrelease.NewConfiguredVerifier(now)
}

// guardManifestVersion is one signed manifest with its Sigstore bundle.
type guardManifestVersion struct {
	raw      []byte
	sig      []byte
	manifest *guardrelease.Manifest
}

// refreshedGuardManifest holds the newest manifest the refresher verified. The
// image-baked manifest in the release directory is only the initial seed.
var refreshedGuardManifest struct {
	sync.RWMutex
	value *guardManifestVersion
}

// currentGuardManifest returns the manifest to serve: the newest of the seed in
// the release directory and the refreshed one. The refreshed manifest is used
// only for the same release version, and only when it was issued later, so the
// served manifest never moves backwards. The reason names the file that failed.
func readVerifiedGuardManifest(dir, manifestFile, bundleFile string) (_ *guardManifestVersion, reason string, _ error) {
	raw, err := readBoundedFile(filepath.Join(dir, manifestFile), guardrelease.MaxManifestBytes)
	if err != nil {
		return nil, "manifest", err
	}
	parsed, err := guardrelease.ParseManifest(raw)
	if err != nil {
		return nil, "manifest_invalid", err
	}
	sig, err := readBoundedFile(filepath.Join(dir, bundleFile), guardrelease.MaxBundleBytes)
	if err != nil {
		return nil, "manifest_bundle", err
	}
	// Authenticate the immutable image pair at its issuance. Delivery paths
	// separately require current freshness; refresh can recover after expiry.
	verifier, err := guardManifestVerifierFactory(func() time.Time { return parsed.IssuedAt })
	if err != nil {
		return nil, "verifier", err
	}
	manifest, err := verifier.VerifyManifest(raw, sig)
	if err != nil {
		return nil, "manifest_unverified", err
	}
	return &guardManifestVersion{raw: raw, sig: sig, manifest: manifest}, "", nil
}

// currentGuardManifest chooses the newest authenticated policy for this image.
func currentGuardManifest(dir string) (_ *guardManifestVersion, reason string, _ error) {
	initial, reason, err := readVerifiedGuardManifest(dir, guardReleaseManifestFile, guardReleaseManifestSig)
	if err != nil {
		return nil, reason, err
	}
	refreshedGuardManifest.RLock()
	refreshed := refreshedGuardManifest.value
	refreshedGuardManifest.RUnlock()
	if refreshed != nil && refreshed.manifest.Version == initial.manifest.Version && refreshed.manifest.IssuedAt.After(initial.manifest.IssuedAt) {
		return refreshed, "", nil
	}
	return initial, "", nil
}

type guardReleaseRefresher struct {
	dir      string
	repo     string
	token    string
	apiBase  string
	client   *http.Client
	verifier guardManifestVerifier
	now      func() time.Time
	log      *logger.Logger
}

// StartGuardReleaseRefresher keeps the served Guard release manifest current
// without an image rebuild. The manifest is baked into the control plane image
// as a seed and ages out (14 days); the release workflow re-signs it nightly and
// publishes it as an asset of the release of the same version. This fetches that
// asset, verifies it with the same pinned signer identity every Guard enforces
// before serving it, and keeps the newest verified one. It does nothing when the
// image carries no signed release or TECHSTACK_GUARD_RELEASE_REFRESH is "off".
// Only the manifest is refreshed: the binaries of a published version never
// change, and a new version goes live only with a new image.
func StartGuardReleaseRefresher(ctx context.Context) {
	dir := guardReleaseDir()
	log := logger.Get().WithComponent("guard-release-refresh")
	// Authenticate both the immutable catalog seed and the image's current
	// policy synchronously before any artifact request can be admitted. A bad
	// image remains available for unrelated routes, while artifact admission
	// continues to fail closed on its own request path.
	if dir != "" {
		if _, _, err := loadArtifactDeliveryIndex(); err != nil {
			log.Error("guard_release_boot_policy_unavailable", "error", err.Error())
		} else if current, _, err := currentGuardManifest(dir); err != nil {
			log.Error("guard_release_boot_policy_unavailable", "error", err.Error())
		} else if verifier, err := guardrelease.NewConfiguredVerifier(nil); err != nil {
			log.Error("guard_release_boot_policy_unavailable", "error", err.Error())
		} else if err := guardrelease.ObserveVerifiedPolicy(current.raw, current.sig, verifier); err != nil {
			log.Warn("guard_release_boot_policy_stale", "error", err.Error())
		}
	}
	if fulllocal.Active() {
		return // the refresh would fetch from GitHub; the image seed is served
	}
	if dir == "" || strings.EqualFold(strings.TrimSpace(os.Getenv(guardReleaseRefreshEnv)), "off") {
		return
	}
	verifier, err := guardrelease.NewConfiguredVerifier(nil)
	if err != nil {
		log.Error("guard_release_refresh_disabled", "reason", "verifier_invalid", "error", err.Error())
		return
	}
	repo := strings.TrimSpace(os.Getenv(guardReleaseSourceEnv))
	if repo == "" {
		repo = defaultGuardReleaseSource
	}
	interval := defaultGuardRefreshInterval
	if raw := strings.TrimSpace(os.Getenv(guardReleaseRefreshIntervalEnv)); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < minGuardRefreshInterval {
			log.Warn("guard_release_refresh_interval_ignored", "value", raw)
		} else {
			interval = parsed
		}
	}
	token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GH_TOKEN"))
	}
	refresher := &guardReleaseRefresher{
		dir: dir, repo: repo, token: token, apiBase: githubAPIBase,
		client:   &http.Client{Timeout: guardReleaseRefreshTimeout},
		verifier: verifier, now: func() time.Time { return time.Now().UTC() }, log: log,
	}
	go refresher.run(ctx, interval)
}

func (r *guardReleaseRefresher) run(ctx context.Context, interval time.Duration) {
	r.cycle(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.cycle(ctx)
		}
	}
}

func (r *guardReleaseRefresher) cycle(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, guardReleaseRefreshTimeout)
	defer cancel()
	updated, err := r.refreshOnce(ctx)
	switch {
	case err != nil:
		r.log.Warn("guard_release_refresh_failed", "error", err.Error())
	case updated:
		r.log.Info("guard_release_manifest_refreshed")
	}
	// A manifest that is more than a day old means the nightly signing or this
	// refresher is not working; Guards refuse updates when it expires.
	if current, _, err := currentGuardManifest(r.dir); err == nil {
		if age := r.now().Sub(current.manifest.IssuedAt); age > guardManifestAgeAlarm {
			r.log.Error("guard_release_manifest_old", "issued_at", current.manifest.IssuedAt.Format(time.RFC3339), "age", age.Round(time.Minute).String())
		}
	}
}

// refreshOnce fetches the release manifest of the served version and, if it
// verifies, is newer and describes the bytes this control plane serves, makes it
// the served manifest. It reports whether the served manifest changed.
func (r *guardReleaseRefresher) refreshOnce(ctx context.Context) (bool, error) {
	current, reason, err := currentGuardManifest(r.dir)
	if err != nil {
		return false, fmt.Errorf("read the served manifest (%s): %w", reason, err)
	}
	raw, sig, err := r.fetchManifest(ctx, current.manifest.Version)
	if err != nil {
		return false, err
	}
	candidate, err := r.verifier.VerifyManifest(raw, sig)
	if err != nil {
		return false, fmt.Errorf("the published manifest does not verify: %w", err)
	}
	if candidate.Version != current.manifest.Version {
		return false, fmt.Errorf("the published manifest is for version %s, not %s", candidate.Version, current.manifest.Version)
	}
	if !candidate.IssuedAt.After(current.manifest.IssuedAt) {
		return false, nil
	}
	if dropped := droppedPolicy(current.manifest, candidate); dropped != "" {
		return false, fmt.Errorf("the published manifest would weaken the served policy: %s", dropped)
	}
	index, _, err := loadArtifactDeliveryIndex()
	if err != nil {
		return false, fmt.Errorf("read the immutable delivery catalog: %w", err)
	}
	for _, artifact := range candidate.Artifacts {
		served, indexed := index.artifact("guard_binary", artifact.OS, artifact.Arch)
		if !indexed || served.SHA256 != artifact.SHA256 || served.Size != artifact.Size {
			return false, fmt.Errorf("the published manifest names another %s/%s binary than this control plane serves", artifact.OS, artifact.Arch)
		}
	}
	r.warnOnBundleDrift(candidate)
	if err := guardrelease.ObserveVerifiedPolicy(raw, sig, r.verifier); err != nil {
		return false, fmt.Errorf("share verified Guard policy with runtime admission: %w", err)
	}
	refreshedGuardManifest.Lock()
	refreshedGuardManifest.value = &guardManifestVersion{raw: raw, sig: sig, manifest: candidate}
	refreshedGuardManifest.Unlock()
	return true, nil
}

// droppedPolicy reports a revocation or minimum version the served manifest has
// and the candidate lacks. Guards never forget a revocation, so the control plane
// does not serve a manifest that silently omits one.
func droppedPolicy(current, candidate *guardrelease.Manifest) string {
	for _, version := range current.RevokedVersions {
		if !containsString(candidate.RevokedVersions, version) {
			return "revoked version " + version + " is missing"
		}
	}
	for _, digest := range current.RevokedDigests {
		if !containsString(candidate.RevokedDigests, digest) {
			return "a revoked digest is missing"
		}
	}
	if order, ok := guardrelease.CompareVersions(candidate.MinimumAdmittedVersion, current.MinimumAdmittedVersion); ok && order < 0 {
		return "the minimum admitted version is lowered"
	}
	return ""
}

// warnOnBundleDrift logs when the manifest names another StackKits runtime bundle
// than the one this control plane serves. The manifest is still served, because
// a revocation must not wait for an image rebuild; Guards refuse the runtime
// install (binding_mismatch) until the image is redeployed.
func (r *guardReleaseRefresher) warnOnBundleDrift(candidate *guardrelease.Manifest) {
	// Read only the immutable signed seed; payloads live in R2. Policy updates
	// still take effect on drift, while runtime admission refuses a stale catalog.
	raw, err := readBoundedFile(filepath.Join(r.dir, guardReleaseSeedManifestFile), guardrelease.MaxManifestBytes)
	if err != nil {
		r.log.Warn("guard_release_seed_unreadable", "error", err.Error())
		return
	}
	seed, err := guardrelease.ParseManifest(raw)
	if err != nil {
		r.log.Warn("guard_release_seed_invalid", "error", err.Error())
		return
	}
	anchor, _ := json.Marshal(seed.StackKits)
	current, _ := json.Marshal(candidate.StackKits)
	if string(anchor) != string(current) {
		r.log.Error("guard_release_bundle_drift", "message", "the signed runtime differs from the immutable delivery catalog; runtime admission is unavailable until catalog migration")
	}
}

type githubRelease struct {
	Assets []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"assets"`
}

func (r *guardReleaseRefresher) fetchManifest(ctx context.Context, version string) (raw, sig []byte, err error) {
	body, err := r.get(ctx, fmt.Sprintf("%s/repos/%s/releases/tags/%s", r.apiBase, r.repo, url.PathEscape("guard-v"+version)), "application/vnd.github+json", 1<<20)
	if err != nil {
		return nil, nil, fmt.Errorf("read the release guard-v%s: %w", version, err)
	}
	var release githubRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return nil, nil, fmt.Errorf("decode the release guard-v%s: %w", version, err)
	}
	asset := func(name string, limit int64) ([]byte, error) {
		for _, candidate := range release.Assets {
			if candidate.Name == name {
				return r.get(ctx, fmt.Sprintf("%s/repos/%s/releases/assets/%d", r.apiBase, r.repo, candidate.ID), "application/octet-stream", limit)
			}
		}
		return nil, fmt.Errorf("release guard-v%s has no %s", version, name)
	}
	if raw, err = asset(guardReleaseManifestFile, guardrelease.MaxManifestBytes); err != nil {
		return nil, nil, err
	}
	if sig, err = asset(guardReleaseManifestSig, guardrelease.MaxBundleBytes); err != nil {
		return nil, nil, err
	}
	return raw, sig, nil
}

func (r *guardReleaseRefresher) get(ctx context.Context, target, accept string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", accept)
	request.Header.Set("User-Agent", "kombify-techstack-guard-release-refresh")
	if r.token != "" {
		request.Header.Set("Authorization", "Bearer "+r.token)
	}
	response, err := r.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("the response exceeds its size bound")
	}
	return body, nil
}
