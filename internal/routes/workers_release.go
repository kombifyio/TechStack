package routes

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/guardrelease"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/logger"
)

const (
	// guardReleaseDirEnv names the directory holding the signed Guard release the
	// control plane publishes: release-manifest.json, its Sigstore bundle, one
	// techstack-<os>-<arch> binary per platform and each binary's bundle. The
	// files come from the Guard release workflow; the control plane serves them
	// unchanged and never signs anything itself.
	guardReleaseDirEnv       = "TECHSTACK_GUARD_RELEASE_DIR"
	guardReleaseManifestFile = "release-manifest.json"
	guardReleaseManifestSig  = "release-manifest.sigstore.json"
	agentBinaryVersionHeader = "X-Kombify-Artifact-Version"
)

var (
	announcedAgentBinaryVersion atomic.Value
	signedArtifactMu            sync.Mutex
	signedArtifactCache         = map[string]agentBinaryArtifact{}
)

// SetAgentBinaryVersion records the version of the executable this control plane
// serves when no signed release is configured. Guards on unsigned local builds
// use it; a signed release carries its version in the signed manifest instead.
func SetAgentBinaryVersion(version string) {
	announcedAgentBinaryVersion.Store(strings.TrimSpace(version))
}

func announcedVersion() string {
	value, _ := announcedAgentBinaryVersion.Load().(string)
	return value
}

// guardReleaseDir returns the configured signed release directory, or "" when
// none is configured or it holds no manifest. The runtime image always sets the
// variable; an image built without a signed release has an empty directory and
// publishes none.
func guardReleaseDir() string {
	dir := strings.TrimSpace(os.Getenv(guardReleaseDirEnv))
	if dir == "" {
		return ""
	}
	if info, err := os.Stat(filepath.Join(dir, guardReleaseManifestFile)); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return dir
}

func signedGuardBinaryName(goos, goarch string) string {
	return fmt.Sprintf("techstack-%s-%s", goos, goarch)
}

// signedGuardBinaryArtifact describes the signed binary of one platform. The
// digest is computed once per file: a deployed release directory is immutable.
func signedGuardBinaryArtifact(dir, goos, goarch string) (agentBinaryArtifact, error) {
	path := filepath.Join(dir, signedGuardBinaryName(goos, goarch))
	signedArtifactMu.Lock()
	defer signedArtifactMu.Unlock()
	if cached, ok := signedArtifactCache[path]; ok {
		return cached, nil
	}
	artifact, err := agentBinaryArtifactForPath(path, goos, goarch)
	if err != nil {
		return agentBinaryArtifact{}, err
	}
	signedArtifactCache[path] = artifact
	return artifact, nil
}

// signedGuardReleaseVersion reads the version of the release directory's manifest.
func signedGuardReleaseVersion(dir string) string {
	raw, err := readBoundedFile(filepath.Join(dir, guardReleaseManifestFile), guardrelease.MaxManifestBytes)
	if err != nil {
		return ""
	}
	manifest, err := guardrelease.ParseManifest(raw)
	if err != nil {
		return ""
	}
	return manifest.Version
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path) // #nosec G304 -- a fixed name below the operator-configured release directory.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("%s is not a bounded regular file", filepath.Base(path))
	}
	return io.ReadAll(io.LimitReader(file, limit+1))
}

// agentReleaseEvidence serves the signed release manifest and the Sigstore
// bundles of the binary next to it, so the Guard's privileged executor can
// authenticate the artifact instead of trusting a checksum served beside it. A
// control plane without a signed release answers 404; a Guard then refuses to
// update unless its host was explicitly configured to admit unsigned builds.
func (h workerRouteHandlers) agentReleaseEvidence(e *httpx.Event) error {
	requestedOS := strings.ToLower(strings.TrimSpace(e.Request.PathValue("os")))
	requestedArch := normalizeAgentBinaryArch(e.Request.PathValue("arch"))
	if requestedOS != managedRuntimeOSLinux || requestedArch == "" {
		return httpx.BadRequest(e, "Unsupported agent platform", nil)
	}
	rawToken := workerRequestCredential(e.Request)
	if rawToken == "" {
		return httpx.Unauthorized(e, "A valid agent download capability is required")
	}
	if _, authorized, err := h.authorizeAgentBinary(e, rawToken, time.Now().UTC()); err != nil || !authorized {
		return err
	}
	dir := guardReleaseDir()
	if dir == "" {
		return httpx.NotFound(e, "This control plane publishes no signed Guard release")
	}
	unavailable := func(reason string, err error) error {
		logger.Get().Error("guard_release_evidence_unavailable", "reason", reason, "error", fmt.Sprint(err))
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "The signed Guard release is temporarily unavailable", nil)
	}
	served, reason, err := currentGuardManifest(dir)
	if err != nil {
		return unavailable(reason, err)
	}
	manifestRaw, manifestSig, manifest := served.raw, served.sig, served.manifest
	entry, found := manifest.Artifact(requestedOS, requestedArch)
	if !found {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The deployed control plane does not publish a signed binary for the requested architecture", nil)
	}
	artifactSig, err := readBoundedFile(filepath.Join(dir, signedGuardBinaryName(requestedOS, requestedArch)+".sigstore.json"), guardrelease.MaxBundleBytes)
	if err != nil {
		return unavailable("artifact_bundle", err)
	}
	artifact, err := signedGuardBinaryArtifact(dir, requestedOS, requestedArch)
	if err != nil {
		return unavailable("artifact", err)
	}
	if artifact.sha256 != entry.SHA256 {
		// The manifest names other bytes than the ones this control plane serves.
		// The Guard would refuse them; do not send evidence that cannot match.
		return unavailable("manifest_artifact_mismatch", fmt.Errorf("manifest %s, file %s", entry.SHA256, artifact.sha256))
	}
	if !json.Valid(manifestSig) || !json.Valid(artifactSig) {
		return unavailable("bundle_invalid", fmt.Errorf("a signature bundle is not JSON"))
	}
	e.Response.Header().Set("Cache-Control", "private, no-store")
	return httpx.Success(e, http.StatusOK, map[string]any{
		"version":         manifest.Version,
		"sha256":          entry.SHA256,
		"manifest":        base64.StdEncoding.EncodeToString(manifestRaw),
		"manifest_bundle": json.RawMessage(manifestSig),
		"artifact_bundle": json.RawMessage(artifactSig),
	})
}

// guardArtifactVersion is the release version of the binary being served.
func guardArtifactVersion() string {
	if dir := guardReleaseDir(); dir != "" {
		return signedGuardReleaseVersion(dir)
	}
	return announcedVersion()
}
