package guardexec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	agentpkg "github.com/kombifyio/techstack/pkg/agent"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/guardrelease"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

const (
	maxProbeOutput   = 4 << 20
	maxManifestProbe = 1 << 20
	probeTimeout     = 15 * time.Second
	// maxRuntimeBundle bounds a StackKits runtime bundle the daemon hands over.
	maxRuntimeBundle int64 = 4 << 30
)

// dockerProbes are the complete set of container probes. The executor builds the
// argv; a caller selects only the kind.
var dockerProbes = map[string][]string{
	DockerProbeVersion:    {"version", "--format", "{{.Server.Version}}"},
	DockerProbeContainers: {"ps", "--all", "--no-trunc", "--format", "{{json .}}"},
	DockerProbeStats:      {"stats", "--no-stream", "--no-trunc", "--format", "{{json .}}"},
}

// HostConfig wires the privileged backend.
type HostConfig struct {
	StackKit *agentpkg.StackKitExecutor
	Updater  *Updater
	// StateDir is the root-only executor state directory.
	StateDir string
	// HandoffDir is the daemon-writable directory the executor reads from.
	HandoffDir string
	// ManifestPaths are the only files probe.file may read.
	ManifestPaths []string
}

// Host is the privileged Backend: it applies the allowlisted operations with
// root's authority, using the same typed StackKits executor the Guard used to run
// in-process.
type Host struct {
	cfg      HostConfig
	observer *Observer
}

// NewHost returns the privileged backend.
func NewHost(cfg HostConfig) (*Host, error) {
	if cfg.StackKit == nil || cfg.Updater == nil || cfg.StateDir == "" || cfg.HandoffDir == "" {
		return nil, errors.New("the privileged backend needs a StackKits executor, an updater and its directories")
	}
	return &Host{cfg: cfg, observer: NewObserver(cfg.StackKit)}, nil
}

// Execute runs one typed command. The observer reports the started process group.
func (h *Host) Execute(ctx context.Context, command *agentpb.StackKitCommand, observe func(ProcessRef), sink func(*agentpb.LogEntry)) *agentpb.StackKitResult {
	ctx = agentpkg.WithProcessObserver(ctx, func(ref agentpkg.ProcessRef) {
		if observe != nil {
			observe(ProcessRef{PID: ref.PID, PGID: ref.PGID, StartTicks: ref.StartTicks})
		}
	})
	return h.cfg.StackKit.ExecuteStreaming(ctx, command, sink)
}

// HostSecurityVerify observes the host baseline through the pinned StackKits CLI.
func (h *Host) HostSecurityVerify(ctx context.Context) (Observation, error) {
	return h.observer.HostSecurityVerify(ctx)
}

// BackupStatus observes the local backup repository through the pinned CLI.
func (h *Host) BackupStatus(ctx context.Context) (Observation, error) {
	return h.observer.BackupStatus(ctx)
}

// Capabilities reports host maintenance when this executor may run it, and the
// host security capability when the pinned StackKits release ships it.
func (h *Host) Capabilities(ctx context.Context) []string {
	capabilities := h.cfg.StackKit.HostMaintenanceCapabilities()
	capabilities = append(capabilities, h.cfg.StackKit.ApplicationCapabilities()...)
	if h.observer.Supported(ctx) {
		capabilities = append(capabilities, stackkitcommand.HostSecurityCapability)
	}
	return capabilities
}

// RuntimeDescribe reports the installed StackKits runtime.
func (h *Host) RuntimeDescribe(context.Context) (RuntimeDescription, error) {
	description := RuntimeDescription{Available: h.cfg.StackKit.Available()}
	if !description.Available {
		return description, nil
	}
	description.InstalledDigest, _ = h.cfg.StackKit.InstalledRuntimeBundleDigest()
	if evidence, err := h.cfg.StackKit.ConfirmInstalledRuntime(); err == nil {
		description.Valid = true
		description.Version, description.ArchiveSHA256 = evidence.Version, evidence.SHA256
	}
	return description, nil
}

// RuntimeInstall copies the handed-over bundle into a file only root can reach,
// checks its digest against the one the caller named, and installs it. The
// daemon cannot change the bytes after the digest check.
func (h *Host) RuntimeInstall(ctx context.Context, params RuntimeInstallParams) (RuntimeEvidence, error) {
	if len(params.SHA256) != sha256.Size*2 {
		return RuntimeEvidence{}, deny(CodeInvalidRequest, "a sha256 is required")
	}
	if err := os.MkdirAll(h.cfg.StateDir, 0o700); err != nil {
		return RuntimeEvidence{}, err
	}
	workDir, err := os.MkdirTemp(h.cfg.StateDir, "runtime-install-*")
	if err != nil {
		return RuntimeEvidence{}, err
	}
	defer func() { _ = os.RemoveAll(workDir) }()
	private := filepath.Join(workDir, "bundle.tar.gz")
	digest, _, err := copyHandoff(h.cfg.HandoffDir, params.BundleFile, private, maxRuntimeBundle)
	if err != nil {
		return RuntimeEvidence{}, err
	}
	if digest != strings.ToLower(params.SHA256) {
		return RuntimeEvidence{}, deny(CodeBindingMismatch, "the bundle does not match the digest the daemon named")
	}
	check, err := h.runtimeCheck(params)
	if err != nil {
		return RuntimeEvidence{}, err
	}
	result, err := h.cfg.StackKit.InstallRuntimeBundleCheckedContext(ctx, private, digest, check)
	if err != nil {
		return RuntimeEvidence{}, err
	}
	return RuntimeEvidence{Version: result.Version, SHA256: result.SHA256}, nil
}

// runtimeCheck authenticates the executables of a StackKits runtime bundle
// before anything from it is activated. The bundle comes from the unprivileged
// daemon and its `stackkit` and `stackkit-server` will run as root, so their
// digests must be the ones a verified, signed release manifest names. A host
// explicitly configured for unsigned local builds skips the check.
func (h *Host) runtimeCheck(params RuntimeInstallParams) (func(stageDir string) error, error) {
	if params.ManifestFile == "" && params.ManifestBundleFile == "" {
		if h.cfg.Updater.AllowsUnsigned() {
			return nil, nil
		}
		return nil, deny(guardrelease.CodeEvidenceMissing, "the StackKits runtime has no signed release evidence and this host admits only signed releases")
	}
	manifest, err := h.cfg.Updater.AuthenticateManifest(params.ManifestFile, params.ManifestBundleFile)
	if err != nil {
		return nil, err
	}
	want := manifest.StackKits
	if want == nil {
		return nil, deny(guardrelease.CodeEvidenceMissing, "the signed release names no StackKits runtime")
	}
	identity, ok := want.ForArch(h.cfg.Updater.cfg.GOARCH)
	if !ok {
		return nil, deny(guardrelease.CodeNotInManifest, "the signed release names no StackKits runtime for %s", h.cfg.Updater.cfg.GOARCH)
	}
	if identity.BundleSHA256 == "" {
		return nil, deny(guardrelease.CodeEvidenceMissing, "the signed release names no digest for the whole StackKits runtime bundle")
	}
	return stackKitsStageCheck(want.Version, identity, h.cfg.StackKit.RuntimeRoot()), nil
}

// stackKitsStageCheck approves an extracted StackKits runtime only when the
// whole tree is the one the signed release names, not only the executables: root
// reads and runs more than the two binaries (the Advanced operations catalog
// defines the argv passed to the signed `stackkit`), so every file is covered by
// the signed tree digest. The pin must also point into the root-owned runtime
// root.
func stackKitsStageCheck(version string, want guardrelease.StackKitsArchRuntime, runtimeRoot string) func(stageDir string) error {
	return func(stage string) error {
		tree, err := guardrelease.TreeDigest(stage)
		if err != nil || tree != want.BundleSHA256 {
			return deny(CodeBindingMismatch, "the StackKits runtime bundle is not the tree the signed release names")
		}
		for name, expected := range map[string]string{"bin/stackkit": want.BinarySHA256, "bin/stackkit-server": want.ServerSHA256} {
			digest, err := fileDigest(filepath.Join(stage, filepath.FromSlash(name)))
			if err != nil || digest != expected {
				return deny(CodeBindingMismatch, "%s in the StackKits runtime is not the executable the signed release names", name)
			}
		}
		pin, err := stackkitrelease.LoadPin(filepath.Join(stage, "stackkits-release-pin.json"))
		if err != nil || pin.Version != version || pin.ArchiveSHA256 != want.ArchiveSHA256 || pin.BinarySHA256 != want.BinarySHA256 {
			return deny(CodeBindingMismatch, "the StackKits runtime pin is not the release the signed manifest names")
		}
		// The pin must point into the runtime root that only root can write, never
		// to a path the daemon could replace between verification and execution.
		if runtimeRoot == "" || filepath.Clean(pin.BinaryPath) != filepath.Join(runtimeRoot, "bin", "stackkit") {
			return deny(CodeBindingMismatch, "the StackKits runtime pin points outside the runtime root")
		}
		return nil
	}
}

// ProbeDocker runs one fixed, read-only container probe.
func (h *Host) ProbeDocker(ctx context.Context, kind string) ([]byte, error) {
	args, ok := dockerProbes[kind]
	if !ok {
		return nil, deny(CodeNotAllowed, "unknown container probe %q", kind)
	}
	path, err := exec.LookPath("docker")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return nil, deny(CodeNotFound, "docker executable is absent")
		}
		return nil, fmt.Errorf("docker is unavailable: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, path, args...) // #nosec G204 -- the argv is a constant of this package selected by a closed kind.
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C", "HOME=/root"}
	var stdout bytes.Buffer
	writer := &limitedWriter{buffer: &stdout, limit: maxProbeOutput}
	command.Stdout = writer
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("docker probe failed: %w", err)
	}
	return limitedProbeOutput(writer)
}

func limitedProbeOutput(writer *limitedWriter) ([]byte, error) {
	if writer.truncated {
		return nil, deny(CodeProbeTruncated, "docker probe output exceeded its bound")
	}
	return writer.buffer.Bytes(), nil
}

// ProbeFile reads one allowlisted host manifest: a regular file, not a symlink,
// of bounded size. Anything else is refused, so the daemon cannot use the
// executor to read arbitrary root-only files.
func (h *Host) ProbeFile(_ context.Context, path string) ([]byte, error) {
	clean := filepath.Clean(path)
	allowed := false
	for _, candidate := range h.cfg.ManifestPaths {
		if filepath.Clean(candidate) == clean {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, deny(CodeNotAllowed, "%s is not a readable host manifest", path)
	}
	info, err := os.Lstat(clean)
	if errors.Is(err, os.ErrNotExist) {
		return nil, deny(CodeNotFound, "%s does not exist", path)
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxManifestProbe {
		return nil, deny(CodeNotAllowed, "%s is not a bounded regular file", path)
	}
	file, err := os.Open(clean) // #nosec G304 -- the path is on the allowlist and was checked not to be a symlink.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	if opened, err := file.Stat(); err != nil || !os.SameFile(info, opened) {
		return nil, deny(CodeNotAllowed, "%s changed while it was opened", path)
	}
	return io.ReadAll(io.LimitReader(file, maxManifestProbe+1))
}

// UpdateStage verifies and stages a signed release.
func (h *Host) UpdateStage(ctx context.Context, params StageParams) (StageResult, error) {
	return h.cfg.Updater.Stage(ctx, params)
}

// UpdateCommit switches to the staged release and starts its trial.
func (h *Host) UpdateCommit(ctx context.Context, params CommitParams) (CommitResult, error) {
	return h.cfg.Updater.Commit(ctx, params)
}

// Status reports the executor's state.
func (h *Host) Status(_ context.Context, syncOperations bool) Status {
	if syncOperations {
		_ = h.cfg.Updater.SyncOperations()
	}
	return h.cfg.Updater.Describe()
}

// DenyMutations reports the recovery flag.
func (h *Host) DenyMutations() bool { return h.cfg.Updater.DenyMutations() }

type limitedWriter struct {
	buffer    *bytes.Buffer
	limit     int
	truncated bool
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit-w.buffer.Len() {
		w.truncated = true
	}
	if room := w.limit - w.buffer.Len(); room > 0 {
		if len(p) > room {
			w.buffer.Write(p[:room])
		} else {
			w.buffer.Write(p)
		}
	}
	return len(p), nil
}

// FileReplay records admitted command identities as files in a root-only
// directory, so a replay is refused even after the executor and the daemon
// restart. Old records are pruned; a command authority never outlives them.
type FileReplay struct {
	Dir string
	// Retention is how long a record is kept; zero selects fourteen days.
	Retention time.Duration
	Now       func() time.Time
}

// Claim records the command ID and reports whether it was new.
func (r *FileReplay) Claim(commandID string) (bool, error) {
	if strings.TrimSpace(commandID) == "" {
		return false, errors.New("a command ID is required")
	}
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return false, err
	}
	sum := sha256.Sum256([]byte(commandID))
	path := filepath.Join(r.Dir, hex.EncodeToString(sum[:]))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- a hashed name below the root-only directory.
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return false, err
	}
	if err := file.Close(); err != nil {
		return false, err
	}
	if err := syncDir(r.Dir); err != nil {
		return false, err
	}
	r.prune()
	return true, nil
}

func (r *FileReplay) prune() {
	retention := r.Retention
	if retention <= 0 {
		retention = 14 * 24 * time.Hour
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	entries, err := os.ReadDir(r.Dir)
	if err != nil || len(entries) < 256 {
		return
	}
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && now.Sub(info.ModTime()) > retention {
			_ = os.Remove(filepath.Join(r.Dir, entry.Name()))
		}
	}
}
