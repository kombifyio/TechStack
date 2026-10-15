package guardexec

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	agentpkg "github.com/kombifyio/techstack/pkg/agent"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

const (
	capabilityCacheTTL = time.Minute
	// syntheticAuthorityMargin extends a self-issued admission past the command
	// timeout, for commands the Guard builds itself (reconciliation).
	syntheticAuthorityMargin = time.Minute
	defaultCommandTimeout    = 30 * time.Minute
	// maxEvidenceResponse bounds the release evidence document a control plane
	// may send: a manifest and two Sigstore bundles.
	maxEvidenceResponse = 4 << 20
)

// Remote is the unprivileged daemon's view of the privileged executor. It
// implements the executor seams the Guard already used in-process, so the
// command path, the journal and the control channel stay as they were while
// every host effect crosses the typed boundary.
type Remote struct {
	Client *Client
	// HandoffDir is the daemon-writable directory the executor reads from.
	HandoffDir string
	Now        func() time.Time

	mu     sync.Mutex
	caps   []string
	capsAt time.Time
}

// NewRemote returns a Remote speaking to the executor socket.
func NewRemote(socket, handoffDir string) *Remote {
	return &Remote{Client: &Client{Socket: socket}, HandoffDir: handoffDir}
}

func (r *Remote) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// Available reports whether the executor answers; the Guard never assumes it.
func (r *Remote) Available(ctx context.Context) bool {
	var status Status
	// A starting daemon also asks the executor to keep the operations
	// executable in step with the Guard executable.
	return r.Client.CallInto(ctx, OpStatus, StatusParams{SyncOperations: true}, &status) == nil && status.Ready
}

// RemoteStatus is the executor's status.
func (r *Remote) RemoteStatus(ctx context.Context) (Status, error) {
	var status Status
	err := r.Client.CallInto(ctx, OpStatus, nil, &status)
	return status, err
}

// Execute runs one typed command through the executor.
func (r *Remote) Execute(ctx context.Context, command *agentpb.StackKitCommand) *agentpb.StackKitResult {
	return r.ExecuteStreaming(ctx, command, nil)
}

// FailureResult is the bounded failure envelope of a command that did not run.
func (r *Remote) FailureResult(command *agentpb.StackKitCommand, err error) *agentpb.StackKitResult {
	return (&agentpkg.StackKitExecutor{}).FailureResult(command, err)
}

// ExecuteStreaming runs one typed command, forwarding its live log records. A
// command the Guard admitted travels with the exact evidence it was admitted
// under; the executor checks it again.
func (r *Remote) ExecuteStreaming(ctx context.Context, command *agentpb.StackKitCommand, sink func(*agentpb.LogEntry)) *agentpb.StackKitResult {
	params, err := r.executeParams(ctx, command)
	if err != nil {
		return r.FailureResult(command, err)
	}
	started := false
	payload, err := r.Client.Call(ctx, OpStackKitExecute, params, func(frame Frame) {
		switch frame.Type {
		case FrameProcess:
			started = true
			if frame.Process != nil {
				if observe, ok := agentpkg.ProcessObserverFrom(ctx); ok {
					observe(agentpkg.ProcessRef{PID: frame.Process.PID, PGID: frame.Process.PGID, StartTicks: frame.Process.StartTicks})
				}
			}
		case FrameLog:
			entry := &agentpb.LogEntry{}
			if sink != nil && protojson.Unmarshal(frame.Payload, entry) == nil {
				sink(entry)
			}
		}
	})
	if err != nil {
		var denial *Error
		switch {
		case ctx.Err() != nil:
			return r.FailureResult(command, ctx.Err())
		case started && (!errors.As(err, &denial) || denial.Code == CodeUnavailable):
			// The effect began and the privileged side vanished before a result:
			// the outcome is unknown, not failed.
			return guardbinding.UnknownOutcomeResult(command.GetCommandId(), "executor_lost_after_start")
		default:
			return r.FailureResult(command, err)
		}
	}
	result := &agentpb.StackKitResult{}
	if err := protojson.Unmarshal(payload, result); err != nil {
		return r.FailureResult(command, fmt.Errorf("the executor sent an unreadable result: %w", err))
	}
	return result
}

func (r *Remote) executeParams(ctx context.Context, command *agentpb.StackKitCommand) (ExecuteParams, error) {
	if admission, ok := guardbinding.AdmissionFrom(ctx); ok && decodesStrictly(admission.Raw) {
		return ExecuteParams{Command: admission.Raw, Binding: admission.Binding}, nil
	}
	// A command the Guard built itself (a reconciliation probe) has no control
	// plane admission; it is bound to its own digest and a short authority.
	raw, err := protojson.Marshal(command)
	if err != nil {
		return ExecuteParams{}, err
	}
	digest, err := guardbinding.CanonicalDigest(raw)
	if err != nil {
		return ExecuteParams{}, err
	}
	timeout := defaultCommandTimeout
	if command.GetTimeoutSeconds() > 0 {
		timeout = time.Duration(command.GetTimeoutSeconds()) * time.Second
	}
	return ExecuteParams{Command: raw, Binding: guardbinding.Binding{
		CommandID: command.GetCommandId(), Operation: command.GetOperation().String(), PayloadDigest: digest,
		AuthorityExpiresAt: r.now().Add(timeout + syntheticAuthorityMargin), ContractVersion: guardbinding.LegacyContractVersion,
	}}, nil
}

func decodesStrictly(raw []byte) bool {
	return protojson.Unmarshal(raw, &agentpb.StackKitCommand{}) == nil
}

// HostMaintenanceCapabilities reports what host maintenance the executor admits,
// cached for a minute so a heartbeat never waits on the socket.
func (r *Remote) HostMaintenanceCapabilities() []string {
	var capabilities []string
	for _, capability := range r.capabilities() {
		if capability != stackkitcommand.ApplicationAdoptionCapability && capability != stackkitcommand.ApplicationConnectorCapability {
			capabilities = append(capabilities, capability)
		}
	}
	return capabilities
}

// ApplicationCapabilities relays the privileged executor's release-derived
// admission through the same cached capability protocol.
func (r *Remote) ApplicationCapabilities() []string {
	var capabilities []string
	for _, capability := range r.capabilities() {
		if capability == stackkitcommand.ApplicationAdoptionCapability || capability == stackkitcommand.ApplicationConnectorCapability {
			capabilities = append(capabilities, capability)
		}
	}
	return capabilities
}

func (r *Remote) capabilities() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.capsAt.IsZero() && r.now().Sub(r.capsAt) < capabilityCacheTTL {
		return append([]string(nil), r.caps...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var response struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := r.Client.CallInto(ctx, OpStackKitCapabilities, nil, &response); err != nil {
		r.caps, r.capsAt = nil, r.now()
		return nil
	}
	r.caps, r.capsAt = response.Capabilities, r.now()
	return append([]string(nil), r.caps...)
}

// ProbeDocker runs a fixed container probe through the executor. name and args
// are the discovery seam's own spelling; only the two known probes map.
func (r *Remote) ProbeDocker(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name != "docker" {
		return nil, fmt.Errorf("probe %q is not a container probe", name)
	}
	kind := ""
	for candidate, argv := range dockerProbes {
		if strings.Join(argv, "\x00") == strings.Join(args, "\x00") {
			kind = candidate
		}
	}
	if kind == "" {
		return nil, fmt.Errorf("container probe %v is not on the allowlist", args)
	}
	var response struct {
		Output []byte `json:"output"`
	}
	if err := r.Client.CallInto(ctx, OpProbeDocker, ProbeDockerParams{Kind: kind}, &response); err != nil {
		return nil, dockerProbeError(err)
	}
	return response.Output, nil
}

// HostSecurityVerify asks the executor for a fresh, read-only observation of the
// host security baseline. The result is the raw StackKits command result; the
// caller judges it.
func (r *Remote) HostSecurityVerify(ctx context.Context) (Observation, error) {
	var observation Observation
	err := r.Client.CallInto(ctx, OpHostSecurityVerify, nil, &observation)
	return observation, err
}

// BackupStatus asks the executor for a read-only observation of the local
// backup repository.
func (r *Remote) BackupStatus(ctx context.Context) (Observation, error) {
	var observation Observation
	err := r.Client.CallInto(ctx, OpBackupStatus, nil, &observation)
	return observation, err
}

// ProbeDockerKind runs one of the executor's fixed container probes by its kind
// (containers or stats).
func (r *Remote) ProbeDockerKind(ctx context.Context, kind string) ([]byte, error) {
	var response struct {
		Output []byte `json:"output"`
	}
	if err := r.Client.CallInto(ctx, OpProbeDocker, ProbeDockerParams{Kind: kind}, &response); err != nil {
		return nil, dockerProbeError(err)
	}
	return response.Output, nil
}

// Keep verified executable absence typed across the privileged wire. Generic
// executor failures and truncation never become a not-applicable runtime.
func dockerProbeError(err error) error {
	if IsCode(err, CodeNotFound) {
		return errors.Join(exec.ErrNotFound, err)
	}
	return err
}

// ReadManifest reads one allowlisted host manifest through the executor. A
// missing file is os.ErrNotExist, like a direct read.
func (r *Remote) ReadManifest(ctx context.Context, path string) ([]byte, error) {
	var response struct {
		Content []byte `json:"content"`
	}
	if err := r.Client.CallInto(ctx, OpProbeFile, ProbeFileParams{Path: path}, &response); err != nil {
		if IsCode(err, CodeNotFound) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	return response.Content, nil
}

// EnsureRuntime converges the StackKits runtime to the bundle the control plane
// publishes.
func (r *Remote) EnsureRuntime(ctx context.Context, cfg agentpkg.StackKitRuntimeBootstrapConfig) error {
	_, err := r.EnsureRuntimeEvidence(ctx, cfg)
	return err
}

// EnsureRuntimeEvidence downloads the published bundle with the Guard's
// credential and lets the executor install it.
func (r *Remote) EnsureRuntimeEvidence(ctx context.Context, cfg agentpkg.StackKitRuntimeBootstrapConfig) (agentpkg.StackKitRuntimeConvergenceResult, error) {
	var description RuntimeDescription
	if err := r.Client.CallInto(ctx, OpRuntimeDescribe, nil, &description); err != nil {
		return agentpkg.StackKitRuntimeConvergenceResult{}, err
	}
	if !description.Available {
		return agentpkg.StackKitRuntimeConvergenceResult{}, errors.New("StackKits runtime paths are not configured")
	}
	cfg.StageDir = r.HandoffDir
	bundle, err := agentpkg.FetchStackKitRuntimeBundle(ctx, cfg, description.InstalledDigest)
	if err != nil {
		return agentpkg.StackKitRuntimeConvergenceResult{}, err
	}
	if bundle.NotModified {
		if !description.Valid {
			return agentpkg.StackKitRuntimeConvergenceResult{}, errors.New("control plane reports current StackKits bundle but local runtime is invalid")
		}
		return agentpkg.StackKitRuntimeConvergenceResult{Version: description.Version, SHA256: description.ArchiveSHA256}, nil
	}
	defer func() { _ = os.Remove(bundle.Path) }()
	install := RuntimeInstallParams{BundleFile: filepath.Base(bundle.Path), SHA256: bundle.SHA256}
	// The executables in this bundle will run as root: hand the executor the
	// signed release evidence it needs to authenticate them. Without it, the
	// executor installs nothing on a host that admits only signed releases.
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	signed, evidenceErr := fetchReleaseEvidence(ctx, client, agentpkg.TechstackRuntimeConvergenceConfig{
		URL: cfg.URL, AgentToken: cfg.AgentToken, RuntimeAgentID: cfg.RuntimeAgentID, TenantID: cfg.TenantID,
	})
	if evidenceErr != nil {
		return agentpkg.StackKitRuntimeConvergenceResult{}, evidenceErr
	}
	if signed != nil {
		manifest, decodeErr := base64.StdEncoding.DecodeString(signed.Manifest)
		if decodeErr != nil {
			return agentpkg.StackKitRuntimeConvergenceResult{}, fmt.Errorf("the release manifest is not base64: %w", decodeErr)
		}
		for name, content := range map[string][]byte{"runtime-manifest.json": manifest, "runtime-manifest.sigstore.json": signed.ManifestBundle} {
			if err := writeHandoffFile(r.HandoffDir, name, content); err != nil {
				return agentpkg.StackKitRuntimeConvergenceResult{}, err
			}
		}
		install.ManifestFile, install.ManifestBundleFile = "runtime-manifest.json", "runtime-manifest.sigstore.json"
	}
	var evidence RuntimeEvidence
	if err := r.Client.CallInto(ctx, OpRuntimeInstall, install, &evidence); err != nil {
		return agentpkg.StackKitRuntimeConvergenceResult{}, err
	}
	return agentpkg.StackKitRuntimeConvergenceResult{Version: evidence.Version, SHA256: evidence.SHA256}, nil
}

// UpdateDeniedError reports that the executor refused a candidate release on
// policy (signature, freshness, digest, revocation, downgrade, failed trial).
// The installed Guard keeps running.
type UpdateDeniedError struct {
	Code    string
	Message string
}

func (e *UpdateDeniedError) Error() string { return "update denied: " + e.Code + ": " + e.Message }

// releaseEvidence is the signed release document of the control plane.
type releaseEvidence struct {
	Manifest       string          `json:"manifest"`
	ManifestBundle json.RawMessage `json:"manifest_bundle"`
	ArtifactBundle json.RawMessage `json:"artifact_bundle"`
	SHA256         string          `json:"sha256"`
	Version        string          `json:"version"`
}

// EnsureTechstackRuntime converges the Guard executables to the signed release
// the control plane publishes. The unprivileged daemon downloads; the executor
// alone authenticates, stages and switches. It never installs an artifact the
// control plane merely vouches for.
func (r *Remote) EnsureTechstackRuntime(ctx context.Context, cfg agentpkg.TechstackRuntimeConvergenceConfig) (agentpkg.TechstackRuntimeConvergenceResult, error) {
	result := agentpkg.TechstackRuntimeConvergenceResult{Version: strings.TrimSpace(cfg.Version)}
	cfg.StageDir = r.HandoffDir
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
		// The installed executable is the one the signed release names.
		return r.confirmCurrent(ctx, result, evidence)
	}
	artifactPath, digest, artifactVersion, notModified, err := agentpkg.FetchTechstackRuntimeArtifact(ctx, cfg, localDigest)
	if err != nil {
		return result, err
	}
	if notModified {
		result.SHA256 = localDigest
		return result, nil
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
			if err := writeHandoffFile(r.HandoffDir, name, content); err != nil {
				return result, err
			}
		}
		params.ManifestFile, params.ManifestBundleFile, params.ArtifactBundleFile = "release-manifest.json", "release-manifest.sigstore.json", "release-artifact.sigstore.json"
	} else {
		// No signed release: only a host whose executor was configured to admit
		// unsigned local builds accepts this, with the version the control
		// plane announced for the artifact.
		params.Version = artifactVersion
	}
	var staged StageResult
	if err := r.Client.CallInto(ctx, OpUpdateStage, params, &staged); err != nil {
		var refusal *Error
		if errors.As(err, &refusal) && refusal.Code != CodeUnavailable && refusal.Code != CodeFailed && refusal.Code != CodeRecoveryNeeded {
			// The executor refused this candidate on policy. The running Guard is
			// unchanged and stays usable; the refusal is reported, not treated as
			// an outage.
			result.SHA256 = localDigest
			return result, &UpdateDeniedError{Code: refusal.Code, Message: refusal.Message}
		}
		return result, err
	}
	result.SHA256 = staged.SHA256
	if staged.AlreadyCurrent {
		return result, nil
	}
	if err := r.Client.CallInto(ctx, OpUpdateCommit, CommitParams{Version: staged.Version, SHA256: staged.SHA256}, nil); err != nil {
		return result, err
	}
	result.AgentUpdated = true
	result.Version = staged.Version
	return result, nil
}

func (r *Remote) confirmCurrent(ctx context.Context, result agentpkg.TechstackRuntimeConvergenceResult, evidence *releaseEvidence) (agentpkg.TechstackRuntimeConvergenceResult, error) {
	result.SHA256 = evidence.SHA256
	// Keep the operations executable in step with the Guard executable.
	var status Status
	_ = r.Client.CallInto(ctx, OpStatus, StatusParams{SyncOperations: true}, &status)
	return result, nil
}

// fetchReleaseEvidence asks the control plane for the signed release manifest
// and signature bundles next to the binary. A nil evidence with no error means
// this control plane publishes no signed release.
func fetchReleaseEvidence(ctx context.Context, client *http.Client, cfg agentpkg.TechstackRuntimeConvergenceConfig) (*releaseEvidence, error) {
	evidenceURL, err := releaseEvidenceURL(cfg.URL)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, evidenceURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.AgentToken))
	request.Header.Set("X-Kombify-Runtime-Agent-ID", strings.TrimSpace(cfg.RuntimeAgentID))
	request.Header.Set("X-Kombify-Tenant-ID", strings.TrimSpace(cfg.TenantID))
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	switch response.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return nil, nil
	case http.StatusOK:
	default:
		return nil, fmt.Errorf("release evidence request returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxEvidenceResponse+1))
	if err != nil || len(body) > maxEvidenceResponse {
		return nil, errors.New("the release evidence is unreadable or exceeds its bound")
	}
	var envelope struct {
		Data releaseEvidence `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode release evidence: %w", err)
	}
	evidence := envelope.Data
	if len(evidence.SHA256) != sha256.Size*2 || evidence.Manifest == "" || len(evidence.ManifestBundle) == 0 || len(evidence.ArtifactBundle) == 0 {
		return nil, errors.New("the release evidence is incomplete")
	}
	if _, err := hex.DecodeString(evidence.SHA256); err != nil {
		return nil, errors.New("the release evidence names no sha256")
	}
	return &evidence, nil
}

// ReleaseEvidenceAvailable reports whether the control plane publishes a signed
// Guard release. A Guard on the privilege-split layout can neither update nor
// install a StackKits runtime without one, so a root Guard migrates only when it
// does.
func ReleaseEvidenceAvailable(ctx context.Context, client *http.Client, cfg agentpkg.TechstackRuntimeConvergenceConfig) (bool, error) {
	if client == nil {
		client = &http.Client{Timeout: time.Minute}
	}
	evidence, err := fetchReleaseEvidence(ctx, client, cfg)
	return evidence != nil, err
}

// releaseEvidenceURL derives the evidence endpoint from the binary endpoint.
func releaseEvidenceURL(binaryURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(binaryURL))
	if err != nil {
		return "", errors.New("the artifact URL is not a control-plane artifact endpoint")
	}
	for _, endpoint := range []string{"/api/v1/agent/binary/", "/api/v1/agent/stackkit-release/"} {
		if strings.Contains(parsed.Path, endpoint) {
			parsed.Path = strings.Replace(parsed.Path, endpoint, "/api/v1/agent/release-evidence/", 1)
			return parsed.String(), nil
		}
	}
	return "", errors.New("the artifact URL is not a control-plane artifact endpoint")
}

func writeHandoffFile(dir, name string, content []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, name), content, 0o600)
}

// ConfirmTrial records that the running Guard completed an authenticated round
// trip. The supervising executor keeps the new Guard only when it sees this.
func ConfirmTrial(handoffDir, runningVersion string, now time.Time) error {
	raw, err := os.ReadFile(filepath.Join(handoffDir, HandoffTrialFile))
	if err != nil {
		return nil // no trial is running
	}
	var trial struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	}
	if json.Unmarshal(raw, &trial) != nil || trial.ID == "" {
		return nil
	}
	if strings.TrimSpace(trial.Version) != strings.TrimSpace(runningVersion) {
		// The running Guard is not the candidate; it confirms nothing.
		return nil
	}
	return writeFileAtomic(filepath.Join(handoffDir, HandoffTrialReadyFile), mustJSON(TrialReady{TrialID: trial.ID, Version: trial.Version, At: now.UTC()}), 0o600)
}

func mustJSON(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

// ReadUpdateStatus returns the executor's published update status.
func ReadUpdateStatus(handoffDir string) UpdateStatus {
	raw, err := os.ReadFile(filepath.Join(handoffDir, HandoffStatusFile))
	if err != nil {
		return UpdateStatus{}
	}
	var status UpdateStatus
	if json.Unmarshal(raw, &status) != nil {
		return UpdateStatus{}
	}
	return status
}
