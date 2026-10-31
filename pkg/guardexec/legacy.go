package guardexec

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	opts               Options
	mu                 sync.Mutex
	runtimeConvergence runtimeConvergence
	host               *Host
	hostErr            error
	candidateServer    *Server
}

// NewLegacyRoot wraps the in-process executor of a root Guard. opts selects the
// state directory, the release trust and the running version; the executable
// paths come from each convergence request.
func NewLegacyRoot(executor *agentpkg.StackKitExecutor, opts Options) *LegacyRoot {
	opts.defaults()
	opts.HandoffDir = filepath.Join(opts.StateDir, legacyHandoffDir)
	l := &LegacyRoot{StackKitExecutor: executor, opts: opts}
	updater, err := NewUpdaterFromOptions(opts)
	if err != nil {
		l.hostErr = err
		return l
	}
	l.host, l.hostErr = NewHost(HostConfig{StackKit: executor, Updater: updater, StateDir: opts.StateDir, HandoffDir: opts.HandoffDir})
	if l.hostErr == nil {
		l.StackKitExecutor = l.host.cfg.StackKit
		l.candidateServer = &Server{Backend: l.host, Replay: &FileReplay{Dir: filepath.Join(opts.StateDir, "admitted")}, Pins: func() ([]guardbinding.PinnedKey, error) {
			return guardbinding.ReadPins(filepath.Join(opts.StateDir, guardbinding.PinFile))
		}}
	}
	return l
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
	l.mu.Lock()
	defer l.mu.Unlock()
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
	staged, err := stageSignedRelease(ctx, updater, opts.HandoffDir, cfg, &result)
	if err != nil {
		return result, err
	}
	if staged == nil || staged.AlreadyCurrent {
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

// stageSignedRelease fetches what the control plane publishes into handoffDir and
// runs it through the updater's release policy. A nil StageResult means the
// installed executable already is that release. A candidate the policy refuses
// is reported as *UpdateDeniedError and changes nothing. result carries the
// digest the caller reports.
func stageSignedRelease(ctx context.Context, updater *Updater, handoffDir string, cfg agentpkg.TechstackRuntimeConvergenceConfig, result *agentpkg.TechstackRuntimeConvergenceResult) (*StageResult, error) {
	localDigest, _ := agentpkg.DigestInstalledExecutable(cfg.AgentPath)
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	evidence, err := fetchReleaseEvidence(ctx, client, cfg)
	if err != nil {
		return nil, err
	}
	if evidence != nil && evidence.SHA256 == localDigest {
		result.SHA256 = localDigest
		return nil, nil
	}
	refusal := func(err error) error {
		var denial *Denial
		if errors.As(err, &denial) {
			result.SHA256 = localDigest
			return &UpdateDeniedError{Code: denial.Code, Message: denial.Message}
		}
		return err
	}
	params := StageParams{}
	if evidence != nil {
		manifest, decodeErr := base64.StdEncoding.DecodeString(evidence.Manifest)
		if decodeErr != nil {
			return nil, fmt.Errorf("the release manifest is not base64: %w", decodeErr)
		}
		files := map[string][]byte{"release-manifest.json": manifest, "release-manifest.sigstore.json": evidence.ManifestBundle, "release-artifact.sigstore.json": evidence.ArtifactBundle}
		for name, content := range files {
			if err := writeHandoffFile(handoffDir, name, content); err != nil {
				return nil, err
			}
		}
		params.ManifestFile, params.ManifestBundleFile, params.ArtifactBundleFile = "release-manifest.json", "release-manifest.sigstore.json", "release-artifact.sigstore.json"
		// A release this host refuses anyway is not downloaded every cycle.
		if err := updater.refuseBeforeDownload(params, evidence.SHA256); err != nil {
			return nil, refusal(err)
		}
	}
	cfg.StageDir = handoffDir
	artifactPath, digest, artifactVersion, notModified, err := agentpkg.FetchTechstackRuntimeArtifact(ctx, cfg, localDigest)
	if err != nil {
		return nil, err
	}
	if notModified {
		result.SHA256 = localDigest
		return nil, nil
	}
	defer func() { _ = os.Remove(artifactPath) }()
	params.ArtifactFile = filepath.Base(artifactPath)
	if evidence == nil {
		params.Version = artifactVersion
	} else if evidence.SHA256 != digest {
		return nil, errors.New("the control plane's signed release does not name the artifact it served")
	}
	staged, err := updater.Stage(ctx, params)
	if err != nil {
		return nil, refusal(err)
	}
	result.SHA256 = staged.SHA256
	return &staged, nil
}

// EnsureRuntime and EnsureRuntimeEvidence override the embedded unprivileged
// bootstrap: legacy root uses the same signed Host install and retention seam.
func (l *LegacyRoot) EnsureRuntime(ctx context.Context, cfg agentpkg.StackKitRuntimeBootstrapConfig) error {
	_, err := l.EnsureRuntimeEvidence(ctx, cfg)
	return err
}
func (l *LegacyRoot) EnsureRuntimeEvidence(ctx context.Context, cfg agentpkg.StackKitRuntimeBootstrapConfig) (agentpkg.StackKitRuntimeConvergenceResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hostErr != nil {
		return agentpkg.StackKitRuntimeConvergenceResult{}, l.hostErr
	}
	cfg.StageDir = l.opts.HandoffDir
	boundary := runtimeBoundary{describe: l.host.RuntimeDescribe, preflight: l.host.RuntimePreflight, install: func(ctx context.Context, p RuntimeInstallParams) (RuntimeEvidence, error) {
		if p.CandidateIntent != nil || p.CandidateAdmission != nil {
			if d := l.candidateServer.admitCandidateInstall(p); d != nil {
				return RuntimeEvidence{}, d
			}
		}
		return l.host.RuntimeInstall(ctx, p)
	}}
	return l.runtimeConvergence.converge(ctx, cfg, boundary, nil)
}
