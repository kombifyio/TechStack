package guardexec

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/drivehealth"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// Backend applies the host effects of the allowlisted operations. It is the only
// privileged code; the Server in front of it decides what reaches it.
type Backend interface {
	Execute(ctx context.Context, command *agentpb.StackKitCommand, observe func(ProcessRef), sink func(*agentpb.LogEntry)) *agentpb.StackKitResult
	Capabilities(ctx context.Context) []string
	RuntimeDescribe(ctx context.Context) (RuntimeDescription, error)
	RuntimeInstall(ctx context.Context, params RuntimeInstallParams) (RuntimeEvidence, error)
	ProbeDocker(ctx context.Context, kind string) ([]byte, error)
	ProbeFile(ctx context.Context, path string) ([]byte, error)
	UpdateStage(ctx context.Context, params StageParams) (StageResult, error)
	UpdateCommit(ctx context.Context, params CommitParams) (CommitResult, error)
	// HostSecurityVerify and BackupStatus are fixed, read-only StackKits
	// observations; neither takes input from the request.
	HostSecurityVerify(ctx context.Context) (Observation, error)
	BackupStatus(ctx context.Context) (Observation, error)
	Status(ctx context.Context, syncOperations bool) Status
	// DenyMutations is true when a failed update left no admissible fallback:
	// every mutating request is then refused until an operator recovers the host.
	DenyMutations() bool
}

// ReplayStore makes one command ID runnable at most once at the privileged
// boundary, independent of the daemon's own journal.
type ReplayStore interface {
	// Claim records the ID and reports whether it was new.
	Claim(commandID string) (fresh bool, err error)
}

// Server serves one connection at a time per call. It owns every decision about
// what may reach the Backend.
type Server struct {
	Backend Backend
	Replay  ReplayStore
	// AllowPeer decides whether the connecting process may use the executor. A
	// nil AllowPeer refuses everyone.
	AllowPeer func(uid uint32) bool
	// Pins returns the binding keys this host pinned at pairing. Once any key
	// is pinned, every binding must carry a valid signature from one of them,
	// so a daemon that was taken over cannot mint its own authority. With no
	// pinned key, observations stay admissible and a mutation is refused
	// until the host is paired again. Nil skips the check.
	Pins func() ([]guardbinding.PinnedKey, error)
	Now  func() time.Time
	Log  *slog.Logger
}

// bindingPinState reads the same pin function the mutation check uses.
func (s *Server) bindingPinState() string {
	if s.Pins == nil {
		return ""
	}
	pins, err := s.Pins()
	if err != nil {
		return guardbinding.BindingPinUnreadable
	}
	if len(pins) == 0 {
		return guardbinding.BindingPinAbsent
	}
	return guardbinding.BindingPinPresent
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Serve handles one request on conn and closes it. The peer's uid is read from
// the kernel, never from the request. When the peer disconnects, the request's
// context is cancelled, which stops the executor's process group.
func (s *Server) Serve(ctx context.Context, conn net.Conn) error {
	defer func() { _ = conn.Close() }()
	out := &frameWriter{w: conn}
	uid, err := PeerUID(conn)
	if err != nil || s.AllowPeer == nil || !s.AllowPeer(uid) {
		s.log().Warn("guard_executor_peer_refused", "uid", uid)
		denied := out.deny(deny(CodePeerNotAllowed, "this process may not use the privileged executor"))
		drainRefused(conn)
		return denied
	}
	reader := bufio.NewReaderSize(conn, 64<<10)
	line, err := readLine(reader, MaxRequestBytes)
	if err != nil {
		return out.deny(deny(CodeInvalidRequest, "the request is missing or exceeds its bound"))
	}
	var request Request
	if err := decodeStrict(line, &request); err != nil || request.Version != ProtocolVersion {
		return out.deny(deny(CodeInvalidRequest, "the request is not a version %d request", ProtocolVersion))
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		// A closed connection means the daemon is gone or gave up: stop the work.
		_, _ = io.Copy(io.Discard, reader)
		cancel()
	}()
	if denial := s.dispatch(ctx, request, out); denial != nil {
		s.logDenial(request.Op, denial)
		return out.deny(denial)
	}
	return nil
}

// logDenial records what the executor answered with. An absent manifest is the
// normal answer of a host that has none of the candidate StackKits paths the
// daemon looks at, so it is not a warning; a failure inside the executor is.
func (s *Server) logDenial(op string, denial *Denial) {
	level := slog.LevelWarn
	switch denial.Code {
	case CodeNotFound:
		level = slog.LevelDebug
	case CodeFailed, CodeUnavailable:
		level = slog.LevelError
	}
	s.log().Log(context.Background(), level, "guard_executor_request_denied", "op", op, "code", denial.Code, "reason", denial.Message)
}

func (s *Server) dispatch(ctx context.Context, request Request, out *frameWriter) *Denial {
	switch request.Op {
	case OpStackKitExecute:
		return s.execute(ctx, request.Params, out)
	case OpStackKitCapabilities:
		return out.result(map[string][]string{"capabilities": s.Backend.Capabilities(ctx)})
	case OpRuntimeDescribe:
		description, err := s.Backend.RuntimeDescribe(ctx)
		if err != nil {
			return deny(CodeFailed, "%v", err)
		}
		return out.result(description)
	case OpRuntimePreflight:
		var params RuntimeInstallParams
		if denial := s.mutationParams(request.Params, &params); denial != nil {
			return denial
		}
		if params.BundleFile != "" {
			return deny(CodeInvalidRequest, "runtime preflight accepts evidence only")
		}
		if params.CandidateIntent != nil || params.CandidateAdmission != nil {
			if denial := s.verifyCandidateInstall(params); denial != nil {
				return denial
			}
		}
		backend, ok := s.Backend.(interface {
			RuntimePreflight(context.Context, RuntimeInstallParams) (RuntimeAdmission, error)
		})
		if !ok {
			return deny(CodeUnavailable, "runtime preflight is unavailable")
		}
		decision, err := backend.RuntimePreflight(ctx, params)
		return resultOrFailure(out, decision, err)
	case OpRuntimeInstall:
		var params RuntimeInstallParams
		if denial := s.mutationParams(request.Params, &params); denial != nil {
			return denial
		}
		if params.CandidateIntent != nil || params.CandidateAdmission != nil {
			if denial := s.admitCandidateInstall(params); denial != nil {
				return denial
			}
		}
		evidence, err := s.Backend.RuntimeInstall(ctx, params)
		return resultOrFailure(out, evidence, err)
	case OpProbeDocker:
		var params ProbeDockerParams
		if err := decodeStrict(request.Params, &params); err != nil {
			return deny(CodeInvalidRequest, "%v", err)
		}
		switch params.Kind {
		case DockerProbeVersion, DockerProbeContainers, DockerProbeStats:
		default:
			return deny(CodeNotAllowed, "unknown container probe %q", params.Kind)
		}
		data, err := s.Backend.ProbeDocker(ctx, params.Kind)
		return resultOrFailure(out, map[string][]byte{"output": data}, err)
	case OpProbeFile:
		var params ProbeFileParams
		if err := decodeStrict(request.Params, &params); err != nil {
			return deny(CodeInvalidRequest, "%v", err)
		}
		data, err := s.Backend.ProbeFile(ctx, params.Path)
		return resultOrFailure(out, map[string][]byte{"content": data}, err)
	case OpProbeDrive:
		var params ProbeDriveParams
		if err := decodeStrict(request.Params, &params); err != nil {
			return deny(CodeInvalidRequest, "%v", err)
		}
		if err := drivehealth.ValidateProbe(params.Kind, params.DevicePath, params.DeviceType); err != nil {
			return deny(CodeNotAllowed, "the drive probe is not admitted")
		}
		backend, ok := s.Backend.(DriveProbeBackend)
		if !ok {
			return deny(CodeUnavailable, "drive telemetry is unavailable on this executor")
		}
		data, err := backend.ProbeDrive(ctx, params.Kind, params.DevicePath, params.DeviceType)
		// Tool/permission failures are observation results. Preserve bounded JSON
		// output when the tool also reports partial telemetry or hardware warnings.
		return out.result(driveProbeResponse{Output: data, Reason: drivehealth.ErrorReason(err)})
	case OpHostSecurityVerify:
		if len(request.Params) > 0 {
			return deny(CodeInvalidRequest, "hostsecurity.verify takes no parameters")
		}
		observation, err := s.Backend.HostSecurityVerify(ctx)
		return resultOrFailure(out, observation, err)
	case OpBackupStatus:
		if len(request.Params) > 0 {
			return deny(CodeInvalidRequest, "backup.status takes no parameters")
		}
		observation, err := s.Backend.BackupStatus(ctx)
		return resultOrFailure(out, observation, err)
	case OpUpdateStage:
		var params StageParams
		if denial := s.mutationParams(request.Params, &params); denial != nil {
			return denial
		}
		staged, err := s.Backend.UpdateStage(ctx, params)
		return resultOrFailure(out, staged, err)
	case OpUpdateCommit:
		var params CommitParams
		if denial := s.mutationParams(request.Params, &params); denial != nil {
			return denial
		}
		committed, err := s.Backend.UpdateCommit(ctx, params)
		return resultOrFailure(out, committed, err)
	case OpStatus:
		var params StatusParams
		if len(request.Params) > 0 {
			if err := decodeStrict(request.Params, &params); err != nil {
				return deny(CodeInvalidRequest, "%v", err)
			}
		}
		status := s.Backend.Status(ctx, params.SyncOperations)
		status.BindingPin = s.bindingPinState()
		return out.result(status)
	default:
		return deny(CodeUnknownOperation, "operation %q is not on the allowlist", request.Op)
	}
}

// mutationParams decodes the parameters of an operation that changes the host
// and refuses it while the host is in recovery.
func (s *Server) mutationParams(raw json.RawMessage, into any) *Denial {
	if s.Backend.DenyMutations() {
		return deny(CodeRecoveryNeeded, "a failed update left no admissible fallback; recover kombify Guard on this host")
	}
	if err := decodeStrict(raw, into); err != nil {
		return deny(CodeInvalidRequest, "%v", err)
	}
	return nil
}

// resultOrFailure maps a backend error to a frame; a *Denial keeps its code.
func resultOrFailure(out *frameWriter, value any, err error) *Denial {
	if err != nil {
		var denial *Denial
		if errors.As(err, &denial) {
			return denial
		}
		return deny(CodeFailed, "%v", err)
	}
	return out.result(value)
}

// execute is the side-effect boundary of a typed command. It re-validates
// everything the daemon claims: the payload is the one admitted, the command is
// well-formed, the authority has not expired and, for a mutation, this command
// identity has not run here before. It never runs an argv supplied by the caller.
func (s *Server) execute(ctx context.Context, raw json.RawMessage, out *frameWriter) *Denial {
	var params ExecuteParams
	if err := decodeStrict(raw, &params); err != nil {
		return deny(CodeInvalidRequest, "%v", err)
	}
	digest, err := guardbinding.CanonicalDigest(params.Command)
	if err != nil || digest != params.Binding.PayloadDigest {
		return deny(CodeBindingMismatch, "the command does not match the digest it was admitted under")
	}
	command := &agentpb.StackKitCommand{}
	if err := (protojson.UnmarshalOptions{}).Unmarshal(params.Command, command); err != nil {
		return deny(CodeInvalidRequest, "the command is not a typed StackKits command")
	}
	if err := stackkitcommand.ValidateCommand(command); err != nil {
		return deny(CodeInvalidRequest, "%v", err)
	}
	binding := params.Binding
	if binding.CommandID == "" || command.GetCommandId() != binding.CommandID || command.Operation.String() != binding.Operation {
		return deny(CodeBindingMismatch, "the command differs from its binding")
	}
	mutating := stackkitcommand.IsMutatingOperation(command.Operation)
	local := binding.ContractVersion == guardbinding.LocalRepairContractVersion
	if params.LocalGrant != nil && !local {
		return deny(CodeBindingMismatch, "only a local repair carries a local repair grant")
	}
	switch binding.ContractVersion {
	case guardbinding.ContractVersion:
	case guardbinding.LocalRepairContractVersion:
		// A repair Guard admitted on its own under the owner's local policy:
		// the signed grant, not a command binding, is its authority.
		if denial := s.admitLocalRepair(params, command); denial != nil {
			return denial
		}
	case guardbinding.LegacyContractVersion:
		// A legacy binding is one the daemon made up itself; it admits
		// observations only.
		if mutating {
			return deny(CodeBindingMismatch, "a mutating command needs a control-plane binding")
		}
	default:
		return deny(CodeBindingMismatch, "binding contract %q is not supported", binding.ContractVersion)
	}
	if s.Pins != nil && !local {
		pins, pinErr := s.Pins()
		if pinErr != nil {
			return deny(CodeUnavailable, "the executor cannot read its pinned binding keys: %v", pinErr)
		}
		// Without a pinned key an unsigned binding verifies, so a process
		// that can reach the socket could forge one. Observations stay
		// admissible; a mutation needs a pinned control-plane key.
		if mutating && len(pins) == 0 {
			return deny(CodeBindingMismatch, "this host pinned no binding signing key; re-pair kombify Guard before running mutating commands")
		}
		if denial := binding.VerifySignature(pins); denial != nil {
			return deny(CodeBindingMismatch, "%s", denial.Message)
		}
	}
	if binding.WorkerGeneration > binding.CurrentGeneration {
		return deny(CodeBindingMismatch, "the binding generation is newer than the credential generation")
	}
	if binding.AuthorityExpiresAt.IsZero() || !s.now().Before(binding.AuthorityExpiresAt) {
		return deny(CodeAuthorityExpired, "the authority to run this command expired")
	}
	if mutating {
		if s.Backend.DenyMutations() {
			return deny(CodeRecoveryNeeded, "a failed update left no admissible fallback; recover kombify Guard on this host")
		}
		if s.Replay == nil {
			return deny(CodeUnavailable, "the executor has no replay record")
		}
		fresh, claimErr := s.Replay.Claim(binding.CommandID)
		if claimErr != nil {
			return deny(CodeUnavailable, "the executor cannot record the command: %v", claimErr)
		}
		if !fresh {
			return deny(CodeReplayed, "command %s was already admitted on this host", binding.CommandID)
		}
	}
	result := s.Backend.Execute(ctx, command,
		func(ref ProcessRef) { _ = out.frame(Frame{Type: FrameProcess, Process: &ref}) },
		func(entry *agentpb.LogEntry) {
			if payload, err := protojson.Marshal(entry); err == nil {
				_ = out.frame(Frame{Type: FrameLog, Payload: payload})
			}
		})
	if result == nil {
		return deny(CodeFailed, "the executor returned no result")
	}
	payload, err := protojson.Marshal(result)
	if err != nil {
		return deny(CodeFailed, "the result cannot be encoded")
	}
	return out.frame(Frame{Type: FrameResult, Payload: payload})
}

// drainRefused lets a refused peer read its denial: closing a socket with unread
// request data would reset the connection and discard the answer.
func drainRefused(conn net.Conn) {
	if closer, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = closer.CloseWrite()
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = io.Copy(io.Discard, io.LimitReader(conn, MaxRequestBytes))
}

// frameWriter serializes frames from the log sink and the handler.
type frameWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (f *frameWriter) frame(frame Frame) *Denial {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := writeRecord(f.w, frame); err != nil {
		return deny(CodeUnavailable, "the daemon went away: %v", err)
	}
	return nil
}

func (f *frameWriter) result(value any) *Denial {
	payload, err := json.Marshal(value)
	if err != nil {
		return deny(CodeFailed, "the result cannot be encoded")
	}
	return f.frame(Frame{Type: FrameResult, Payload: payload})
}

// deny answers the client with the structured denial. Refusing a request and
// telling the client why is the executor working as designed, so the handler
// returns nil and the socket-activated instance exits 0: a non-zero exit would
// leave a failed unit (and a degraded host) for every refused or not-found
// request. Only an answer that could not be delivered is a fault of this
// instance and is returned; the denial itself is logged by the caller.
func (f *frameWriter) deny(d *Denial) error {
	if err := f.frame(Frame{Type: FrameError, Code: d.Code, Message: d.Message}); err != nil {
		return fmt.Errorf("deliver the %s denial: %w", d.Code, err)
	}
	return nil
}
