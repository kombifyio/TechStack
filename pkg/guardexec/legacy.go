package guardexec

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentpkg "github.com/kombifyio/techstack/pkg/agent"
)

// legacyHandoffDir is where a root Guard stages a downloaded release before it
// authenticates it. It lives below the root-only state directory: on a legacy
// host there is no unprivileged daemon to share it with.
const legacyHandoffDir = "legacy-handoff"

// LegacyRoot is a Guard that still runs as root (legacy_root). It applies host
// effects in process like the plain StackKits executor, but it converges its
// own executable under the same release policy as the privileged executor: only
// to a release the pinned identity signed and the manifest floor admits, or to
// an unsigned build when its own environment sets
// TECHSTACK_GUARD_ALLOW_UNSIGNED_UPDATES. The executable it replaces is kept as
// .prev. It never installs an artifact the control plane merely vouches for.
type LegacyRoot struct {
	*agentpkg.StackKitExecutor
	opts Options
}

// NewLegacyRoot wraps the in-process executor of a root Guard. opts selects the
// state directory, the release trust and the running version; the executable
// paths come from each convergence request.
func NewLegacyRoot(executor *agentpkg.StackKitExecutor, opts Options) *LegacyRoot {
	opts.defaults()
	return &LegacyRoot{StackKitExecutor: executor, opts: opts}
}

// AdmitsUnsigned reports whether this Guard's own environment admits unsigned
// builds.
func (l *LegacyRoot) AdmitsUnsigned() bool {
	return envTrue(l.opts.Getenv("TECHSTACK_GUARD_ALLOW_UNSIGNED_UPDATES"))
}

// EnsureTechstackRuntime converges the Guard and operations executables to the
// signed release the control plane publishes. A candidate the policy refuses
// leaves both executables as they are and is reported as *UpdateDeniedError.
func (l *LegacyRoot) EnsureTechstackRuntime(ctx context.Context, cfg agentpkg.TechstackRuntimeConvergenceConfig) (agentpkg.TechstackRuntimeConvergenceResult, error) {
	result := agentpkg.TechstackRuntimeConvergenceResult{Version: strings.TrimSpace(cfg.Version)}
	for label, path := range map[string]string{"agent": cfg.AgentPath, "operations": cfg.OperationsPath} {
		if !filepath.IsAbs(filepath.Clean(path)) {
			return result, fmt.Errorf("techstack %s executable path must be absolute", label)
		}
	}
	opts := l.opts
	opts.AgentPath, opts.OperationsPath = cfg.AgentPath, cfg.OperationsPath
	opts.HandoffDir = filepath.Join(opts.StateDir, legacyHandoffDir)
	if err := os.MkdirAll(opts.HandoffDir, 0o700); err != nil {
		return result, err
	}
	updater, err := NewUpdaterFromOptions(opts)
	if err != nil {
		return result, err
	}
	localDigest, _ := agentpkg.DigestInstalledExecutable(cfg.AgentPath)
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	evidence, err := fetchReleaseEvidence(ctx, client, cfg)
	if err != nil {
		return result, err
	}
	if evidence != nil && evidence.SHA256 == localDigest {
		result.SHA256 = localDigest
		return result, updater.SyncOperations()
	}
	cfg.StageDir = opts.HandoffDir
	artifactPath, digest, artifactVersion, notModified, err := agentpkg.FetchTechstackRuntimeArtifact(ctx, cfg, localDigest)
	if err != nil {
		return result, err
	}
	if notModified {
		result.SHA256 = localDigest
		return result, updater.SyncOperations()
	}
	defer func() { _ = os.Remove(artifactPath) }()
	params := StageParams{ArtifactFile: filepath.Base(artifactPath)}
	if evidence != nil {
		if evidence.SHA256 != digest {
			return result, errors.New("the control plane's signed release does not name the artifact it served")
		}
		manifest, decodeErr := base64.StdEncoding.DecodeString(evidence.Manifest)
		if decodeErr != nil {
			return result, fmt.Errorf("the release manifest is not base64: %w", decodeErr)
		}
		files := map[string][]byte{"release-manifest.json": manifest, "release-manifest.sigstore.json": evidence.ManifestBundle, "release-artifact.sigstore.json": evidence.ArtifactBundle}
		for name, content := range files {
			if err := writeHandoffFile(opts.HandoffDir, name, content); err != nil {
				return result, err
			}
		}
		params.ManifestFile, params.ManifestBundleFile, params.ArtifactBundleFile = "release-manifest.json", "release-manifest.sigstore.json", "release-artifact.sigstore.json"
	} else {
		params.Version = artifactVersion
	}
	staged, err := updater.Stage(ctx, params)
	if err != nil {
		var denial *Denial
		if errors.As(err, &denial) {
			result.SHA256 = localDigest
			return result, &UpdateDeniedError{Code: denial.Code, Message: denial.Message}
		}
		return result, err
	}
	result.SHA256 = staged.SHA256
	if staged.AlreadyCurrent {
		return result, updater.SyncOperations()
	}
	// Unlike the split executor there is no supervised trial: the caller
	// restarts the Guard, and .prev is kept for a manual rollback.
	if err := updater.switchTo(".next"); err != nil {
		return result, err
	}
	_ = os.Remove(updater.path(stagedFile))
	_ = updater.record(UpdateStatus{State: StateCommitted, Version: staged.Version, PreviousVersion: opts.Version})
	result.AgentUpdated, result.OperationsUpdated = true, true
	result.Version = staged.Version
	return result, nil
}
