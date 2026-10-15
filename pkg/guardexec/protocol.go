// Package guardexec is the boundary between the unprivileged kombify Guard
// daemon and the privileged executor that applies host effects.
//
// The daemon owns the network, the host identity, the access token and the
// durable journal, and runs as a dedicated system user with a hardened unit.
// Everything that needs root runs in a separate executor process that the
// service manager starts per connection on a Unix socket only the daemon's
// group can reach. The executor accepts a closed allowlist of typed requests,
// re-validates each one at the side-effect boundary, builds its arguments
// itself (never from a shell string) and streams bounded output back. It holds
// no credential and has no network client.
package guardexec

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/kombifyio/techstack/pkg/guardbinding"
)

// ProtocolVersion is the wire version both sides must speak.
const ProtocolVersion = 1

const (
	// MaxRequestBytes bounds one request; a typed command may carry a
	// candidate StackSpec.
	MaxRequestBytes = 48 << 20
	// MaxFrameBytes bounds one response frame; a StackKits result can carry
	// 16 MiB of output and 16 MiB of events, base64 encoded.
	MaxFrameBytes = 96 << 20
)

// Operations are the complete allowlist. Anything else is refused.
const (
	// OpStackKitExecute runs one typed StackKits command.
	OpStackKitExecute = "stackkit.execute"
	// OpStackKitCapabilities reports what host maintenance this host admits.
	OpStackKitCapabilities = "stackkit.capabilities"
	// OpRuntimeDescribe reports the installed StackKits runtime.
	OpRuntimeDescribe = "stackkit.runtime.describe"
	// OpRuntimeInstall installs a downloaded StackKits runtime bundle.
	OpRuntimeInstall = "stackkit.runtime.install"
	// OpProbeDocker runs one of the fixed read-only container probes.
	OpProbeDocker = "probe.docker"
	// OpProbeDrive runs a fixed read-only SMART/NVMe observation.
	OpProbeDrive = "probe.drive"
	// OpProbeFile reads one allowlisted host manifest.
	OpProbeFile = "probe.file"
	// OpUpdateStage verifies and stages a signed Guard release.
	OpUpdateStage = "update.stage"
	// OpUpdateCommit switches to the staged release and starts its trial.
	OpUpdateCommit = "update.commit"
	// OpStatus reports the executor and update state.
	OpStatus = "status"
	// OpHostSecurityVerify runs `stackkit host security verify`, a read-only
	// observation of the host baseline, with a fixed argument vector.
	OpHostSecurityVerify = "hostsecurity.verify"
	// OpBackupStatus runs `stackkit backup status`, a read-only observation of
	// the local backup repository, with a fixed argument vector.
	OpBackupStatus = "backup.status"
)

// Frame types of a response stream.
const (
	FrameProcess = "process"
	FrameLog     = "log"
	FrameResult  = "result"
	FrameError   = "error"
)

// Stable denial codes.
const (
	CodeInvalidRequest   = "invalid_request"
	CodeUnknownOperation = "unknown_operation"
	CodePeerNotAllowed   = "peer_not_allowed"
	CodeBindingMismatch  = "binding_mismatch"
	CodeReplayed         = "replayed"
	CodeAuthorityExpired = "authority_expired"
	CodeRecoveryNeeded   = "recovery_needed"
	CodeNotAllowed       = "not_allowed"
	CodeNotFound         = "not_found"
	CodeUnavailable      = "executor_unavailable"
	CodeFailed           = "failed"
	// CodeProbeTruncated means bounded output cannot establish full discovery.
	CodeProbeTruncated = "probe_output_truncated"
)

// Request is one typed request. Params is decoded strictly per operation.
type Request struct {
	Version int             `json:"v"`
	Op      string          `json:"op"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Frame is one response record.
type Frame struct {
	Type    string          `json:"t"`
	Process *ProcessRef     `json:"process,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Code    string          `json:"code,omitempty"`
	Message string          `json:"message,omitempty"`
}

// ProcessRef identifies the started executor process group, so a restarted
// daemon can record it in its journal.
type ProcessRef struct {
	PID        int    `json:"pid"`
	PGID       int    `json:"pgid"`
	StartTicks uint64 `json:"start_ticks,omitempty"`
}

// Denial is a structured refusal of the executor.
type Denial struct {
	Code    string
	Message string
}

func (d *Denial) Error() string { return d.Code + ": " + d.Message }

func deny(code, format string, args ...any) *Denial {
	return &Denial{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ExecuteParams carries one typed command with the evidence it was admitted under.
type ExecuteParams struct {
	// Command is the exact JSON the binding's payload digest names.
	Command json.RawMessage      `json:"command"`
	Binding guardbinding.Binding `json:"binding"`
}

// RuntimeDescription is the installed StackKits runtime.
type RuntimeDescription struct {
	Available       bool   `json:"available"`
	InstalledDigest string `json:"installed_digest,omitempty"`
	Valid           bool   `json:"valid"`
	Version         string `json:"version,omitempty"`
	ArchiveSHA256   string `json:"archive_sha256,omitempty"`
}

// RuntimeInstallParams hands a downloaded bundle to the executor. The path names
// a file in the handoff directory; the executor copies it before it reads it.
type RuntimeInstallParams struct {
	BundleFile string `json:"bundle_file"`
	SHA256     string `json:"sha256"`
	// ManifestFile and ManifestBundleFile are the signed release evidence. The
	// executor installs the bundle only when its executables are the StackKits
	// runtime the verified manifest names.
	ManifestFile       string `json:"manifest_file,omitempty"`
	ManifestBundleFile string `json:"manifest_bundle_file,omitempty"`
}

// RuntimeEvidence is the exact StackKits artifact identity after installation.
type RuntimeEvidence struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

// Docker probes are fixed; the executor builds the argv.
const (
	DockerProbeVersion    = "version"
	DockerProbeContainers = "containers"
	// DockerProbeStats is one point-in-time resource reading of the running
	// containers (`docker stats --no-stream`).
	DockerProbeStats = "stats"
)

// Stable reasons an observation could not run.
const (
	ReasonRuntimeUnavailable   = "stackkit_runtime_unavailable"
	ReasonCLIUnsupported       = "stackkit_cli_unsupported"
	ReasonWorkspaceUnavailable = "workspace_unavailable"
)

// Observation is the bounded, redacted outcome of a read-only StackKits
// observation. Supported is false when the pinned StackKits release does not
// ship the command: that is an observation gap, not a failure.
type Observation struct {
	Supported      bool   `json:"supported"`
	ExitCode       int    `json:"exit_code"`
	Stdout         []byte `json:"stdout,omitempty"`
	Stderr         string `json:"stderr,omitempty"`
	ReleaseVersion string `json:"release_version,omitempty"`
	// Reason is a stable code when the observation could not run:
	// stackkit_runtime_unavailable, stackkit_cli_unsupported or
	// workspace_unavailable.
	Reason string `json:"reason,omitempty"`
}

// ProbeDockerParams selects one fixed container probe.
type ProbeDockerParams struct {
	Kind string `json:"kind"`
}

// ProbeDriveParams selects a fixed telemetry command, never caller-supplied argv.
type ProbeDriveParams struct {
	Kind       string `json:"kind"`
	DevicePath string `json:"device_path,omitempty"`
	DeviceType string `json:"device_type,omitempty"`
}

// ProbeFileParams names one host manifest to read.
type ProbeFileParams struct {
	Path string `json:"path"`
}

// StageParams names the release evidence in the handoff directory.
type StageParams struct {
	ManifestFile       string `json:"manifest_file,omitempty"`
	ManifestBundleFile string `json:"manifest_bundle_file,omitempty"`
	ArtifactFile       string `json:"artifact_file"`
	ArtifactBundleFile string `json:"artifact_bundle_file,omitempty"`
	// Version names the release of an artifact that carries no evidence. It is
	// honored only on a host whose executor was explicitly configured to admit
	// unsigned local builds.
	Version string `json:"version,omitempty"`
}

// StageResult is an artifact that passed verification and is staged.
type StageResult struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	// AlreadyCurrent is true when the verified artifact is the installed one;
	// nothing was staged and there is nothing to commit.
	AlreadyCurrent bool `json:"already_current,omitempty"`
}

// CommitParams names the staged release to switch to.
type CommitParams struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

// CommitResult reports that the switch was made and a trial supervises it.
type CommitResult struct {
	TrialID string `json:"trial_id"`
}

// StatusParams are the optional parameters of a status request.
type StatusParams struct {
	// SyncOperations also makes the operations executable byte-identical to the
	// Guard executable. It hashes both binaries, so only a daemon that has just
	// started asks for it.
	SyncOperations bool `json:"sync_operations,omitempty"`
}

// Status is the executor's own view of itself.
type Status struct {
	Ready         bool   `json:"ready"`
	Privileged    bool   `json:"privileged"`
	UpdateState   string `json:"update_state,omitempty"`
	UpdateVersion string `json:"update_version,omitempty"`
	UpdateReason  string `json:"update_reason,omitempty"`
	// DenyMutations is true after a failed update left no admissible fallback.
	DenyMutations bool `json:"deny_mutations"`
	// AdmitsUnsigned is true when the host was explicitly configured to admit
	// unsigned local builds; such a Guard is never reported as signed-only.
	AdmitsUnsigned bool `json:"admits_unsigned,omitempty"`
}

func decodeStrict(raw json.RawMessage, into any) error {
	if len(raw) == 0 {
		return errors.New("params are required")
	}
	decoder := json.NewDecoder(bytesReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("params contain trailing data")
	}
	return nil
}

// IsDenial reports whether err is an executor denial with the given code.
func IsDenial(err error, code string) bool {
	var denial *Denial
	return errors.As(err, &denial) && denial.Code == code
}
