package guardexec

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// localRepairTimeout bounds the CLI of a local repair, as for an online one.
const localRepairTimeout = 10 * time.Minute

// ErrLocalRepairNotStarted means a local repair was refused or could not reach
// the executor before any effect: nothing changed on the host.
var ErrLocalRepairNotStarted = errors.New("the local repair did not start")

// LocalRepairCommand is the typed StackKits host-security repair of controls
// under a local repair grant: the same command the control plane would send,
// with the operation ID as its command ID and the release the grant names.
func LocalRepairCommand(grant guardbinding.LocalRepairGrant, operationID string, controls []string) (*agentpb.StackKitCommand, error) {
	release := grant.Release
	command := &agentpb.StackKitCommand{
		CommandId:      operationID,
		Operation:      agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_SECURITY_REPAIR,
		TimeoutSeconds: int32(localRepairTimeout / time.Second),
		Release: &agentpb.StackKitReleasePin{
			Version: release.Version, PlatformOs: release.PlatformOS, PlatformArch: release.PlatformArch,
			ArchiveSha256: release.ArchiveSHA256, ReleaseIndexSha256: release.ReleaseIndexSHA256,
		},
		// The owner's approval is the policy version the grant names.
		OwnerApproved:        true,
		HostSecurityControls: append([]string(nil), controls...),
	}
	if err := stackkitcommand.ValidateCommand(command); err != nil {
		return nil, err
	}
	return command, nil
}

// LocalRepairParams is the executor request of one local repair: the exact
// command, a binding of the local repair contract naming the operation, and
// the signed grant the executor verifies itself.
func LocalRepairParams(grant guardbinding.LocalRepairGrant, operationID string, controls []string) (ExecuteParams, *agentpb.StackKitCommand, error) {
	command, err := LocalRepairCommand(grant, operationID, controls)
	if err != nil {
		return ExecuteParams{}, nil, err
	}
	raw, err := protojson.Marshal(command)
	if err != nil {
		return ExecuteParams{}, nil, err
	}
	digest, err := guardbinding.CanonicalDigest(raw)
	if err != nil {
		return ExecuteParams{}, nil, err
	}
	local := grant
	return ExecuteParams{Command: raw, LocalGrant: &local, Binding: guardbinding.Binding{
		CommandID: operationID, TenantID: grant.TenantID, WorkerID: grant.WorkerID, ServerID: grant.ServerID,
		Operation: command.GetOperation().String(), PayloadDigest: digest,
		WorkerGeneration: grant.Generation, CurrentGeneration: grant.Generation,
		AuthorityExpiresAt: grant.AuthorityUntil(), ContractVersion: guardbinding.LocalRepairContractVersion,
	}}, command, nil
}

// admitLocalRepair is the executor's check of a request of the local repair
// contract. A local repair is never admitted without a pinned key: the grant's
// signature is the only authority behind it.
func (s *Server) admitLocalRepair(params ExecuteParams, command *agentpb.StackKitCommand) *Denial {
	grant := params.LocalGrant
	if grant == nil {
		return deny(CodeBindingMismatch, "a local repair needs its signed grant")
	}
	if s.Pins == nil {
		return deny(CodeBindingMismatch, "this executor holds no pinned binding keys, so it admits no local repair")
	}
	pins, err := s.Pins()
	if err != nil {
		return deny(CodeUnavailable, "the executor cannot read its pinned binding keys: %v", err)
	}
	binding := params.Binding
	release := command.GetRelease()
	switch {
	case command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_SECURITY_REPAIR || !command.OwnerApproved:
		return deny(CodeNotAllowed, "a local policy admits only the host security repair")
	case binding.TenantID != grant.TenantID || binding.WorkerID != grant.WorkerID || binding.ServerID != grant.ServerID ||
		binding.WorkerGeneration != grant.Generation || binding.AuthorityExpiresAt.After(grant.AuthorityUntil()):
		return deny(CodeBindingMismatch, "the local repair differs from its grant")
	case release == nil || release.Version != grant.Release.Version || release.PlatformOs != grant.Release.PlatformOS ||
		release.PlatformArch != grant.Release.PlatformArch || release.ArchiveSha256 != grant.Release.ArchiveSHA256 ||
		release.ReleaseIndexSha256 != grant.Release.ReleaseIndexSHA256:
		return deny(CodeBindingMismatch, "the local repair names another release than its grant")
	}
	if denial := grant.AdmitAtExecutor(pins, binding.CommandID, command.HostSecurityControls, s.now()); denial != nil {
		code := CodeBindingMismatch
		if denial.Code == guardbinding.CodeAuthorityExpired {
			code = CodeAuthorityExpired
		}
		return deny(code, "%s", denial.Message)
	}
	return nil
}

// LocalHostSecurityRepair runs one local repair through the privileged
// executor, which verifies the grant and the operation itself and runs it at
// most once. It returns ErrLocalRepairNotStarted when the executor refused it
// or could not be reached before any effect, and an unknown-outcome result when
// the executor vanished after the effect began.
func (r *Remote) LocalHostSecurityRepair(ctx context.Context, grant guardbinding.LocalRepairGrant, operationID string, controls []string) (*agentpb.StackKitResult, error) {
	params, command, err := LocalRepairParams(grant, operationID, controls)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLocalRepairNotStarted, err)
	}
	started := false
	payload, err := r.Client.Call(ctx, OpStackKitExecute, params, func(frame Frame) {
		if frame.Type == FrameProcess {
			started = true
		}
	})
	if err != nil {
		var denial *Error
		if started && (ctx.Err() != nil || !errors.As(err, &denial) || denial.Code == CodeUnavailable) {
			return guardbinding.UnknownOutcomeResult(command.GetCommandId(), "executor_lost_after_start"), nil
		}
		return nil, fmt.Errorf("%w: %v", ErrLocalRepairNotStarted, err)
	}
	result := &agentpb.StackKitResult{}
	if err := protojson.Unmarshal(payload, result); err != nil {
		return guardbinding.UnknownOutcomeResult(command.GetCommandId(), "unreadable_result"), nil
	}
	return result, nil
}
