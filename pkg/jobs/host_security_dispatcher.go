package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/agentcontrol"
	agentpb "github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/guardposture"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

const (
	// hostSecurityRepairTimeout bounds the CLI; the Guard gets headroom on top.
	hostSecurityRepairTimeout = 10 * time.Minute
	hostSecurityRepairGrace   = time.Minute
	hostSecurityRepairSchema  = "stackkit.host-security-repair/v1"
)

var hostSecurityReasonToken = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// HostSecurityRepairDispatcher runs `stackkit host security repair --apply` on a
// node's Guard through the typed StackKit command channel. The hub refuses a
// Guard that does not advertise stackkit.host-security.v1.
type HostSecurityRepairDispatcher struct {
	sender          StackKitCommandSender
	releaseResolver func() (*stackkitrelease.Release, error)
}

var _ guardposture.RepairDispatcher = (*HostSecurityRepairDispatcher)(nil)

// NewHostSecurityRepairDispatcher returns nil without a sender, which keeps
// repair unavailable.
func NewHostSecurityRepairDispatcher(sender StackKitCommandSender) *HostSecurityRepairDispatcher {
	if sender == nil {
		return nil
	}
	return &HostSecurityRepairDispatcher{sender: sender, releaseResolver: configuredTargetStackKitRelease}
}

// LazyHostSecurityRepairDispatcher resolves the StackKit command sender at
// request time. The commander is installed after the posture service is
// composed at boot, so binding it eagerly would leave repair unavailable for
// the life of the process.
type LazyHostSecurityRepairDispatcher struct {
	resolve func() StackKitCommandSender
}

var _ guardposture.RepairDispatcher = (*LazyHostSecurityRepairDispatcher)(nil)

// NewLazyHostSecurityRepairDispatcher returns a dispatcher that asks resolve
// for the sender on every use. A nil resolve keeps repair unavailable.
func NewLazyHostSecurityRepairDispatcher(resolve func() StackKitCommandSender) *LazyHostSecurityRepairDispatcher {
	if resolve == nil {
		return nil
	}
	return &LazyHostSecurityRepairDispatcher{resolve: resolve}
}

// Available reports whether a sender is installed right now.
func (d *LazyHostSecurityRepairDispatcher) Available() bool {
	return d != nil && d.current() != nil
}

func (d *LazyHostSecurityRepairDispatcher) current() *HostSecurityRepairDispatcher {
	if d == nil || d.resolve == nil {
		return nil
	}
	return NewHostSecurityRepairDispatcher(d.resolve())
}

// Repair delegates to the dispatcher built from the sender installed now.
func (d *LazyHostSecurityRepairDispatcher) Repair(ctx context.Context, target guardposture.RepairTarget) (guardposture.RepairResult, error) {
	return d.current().Repair(ctx, target)
}

// Repair sends the command for exactly the named controls and waits for the
// Guard's result.
func (d *HostSecurityRepairDispatcher) Repair(ctx context.Context, target guardposture.RepairTarget) (guardposture.RepairResult, error) {
	if d == nil || d.sender == nil {
		return guardposture.RepairResult{}, errors.New("host security repair dispatch is not available")
	}
	release, err := d.releaseResolver()
	if err != nil {
		return guardposture.RepairResult{}, fmt.Errorf("resolve pinned StackKits release: %w", err)
	}
	if release == nil {
		return guardposture.RepairResult{}, errors.New("pinned published StackKits release is not configured")
	}
	command := &agentpb.StackKitCommand{
		CommandId:            target.CommandID,
		Operation:            agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_SECURITY_REPAIR,
		TimeoutSeconds:       int32(hostSecurityRepairTimeout / time.Second),
		Release:              grpcserver.StackKitReleasePinFor(*release),
		OwnerApproved:        true,
		HostSecurityControls: target.Controls,
	}
	if err := stackkitcommand.ValidateCommand(command); err != nil {
		return guardposture.RepairResult{}, err
	}
	result, err := sendStackKitCommandBoundedWithGraceForTenant(ctx, d.sender, target.TenantID, target.AgentID, command, hostSecurityRepairGrace)
	if errors.Is(err, agentcontrol.ErrCommandCancelledBeforeDispatch) {
		return guardposture.RepairResult{}, fmt.Errorf("%w: %v", guardposture.ErrRepairNotDelivered, err)
	}
	if err != nil {
		return guardposture.RepairResult{}, err
	}
	if result == nil {
		return guardposture.RepairResult{}, errors.New("host security repair returned no result")
	}
	return parseHostSecurityRepair(result)
}

type hostSecurityRepairDocument struct {
	SchemaVersion string `json:"schema_version"`
	Outcome       string `json:"outcome"`
	Steps         []struct {
		Control    string `json:"control"`
		Status     string `json:"status"`
		ReasonCode string `json:"reason_code"`
	} `json:"steps"`
}

// parseHostSecurityRepair reads the stackkit.host-security-repair/v1 document
// out of the command result. A document is bounded and closed to the known
// vocabulary before it is stored; free text from the host never is.
func parseHostSecurityRepair(result *agentpb.StackKitResult) (guardposture.RepairResult, error) {
	var envelope struct {
		Status string          `json:"status"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(result.CommandResultJson, &envelope); err != nil {
		return guardposture.RepairResult{}, fmt.Errorf("decode host security repair result: %w", err)
	}
	var document hostSecurityRepairDocument
	if json.Unmarshal(envelope.Data, &document) != nil || document.SchemaVersion != hostSecurityRepairSchema {
		return guardposture.RepairResult{}, fmt.Errorf("host security repair returned no %s document (exit %d)", hostSecurityRepairSchema, result.ExitCode)
	}
	steps := make([]guardposture.RepairStep, 0, len(document.Steps))
	for _, step := range document.Steps {
		if len(steps) == len(stackkitcommand.HostSecurityControlIDs) || !stackkitcommand.ValidHostSecurityControl(step.Control) {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(step.Status))
		switch status {
		case "planned", "applied", "noop", "manual", "blocked", "failed":
		default:
			status = "failed"
		}
		reason := strings.ToLower(strings.TrimSpace(step.ReasonCode))
		if !hostSecurityReasonToken.MatchString(reason) {
			reason = ""
		}
		steps = append(steps, guardposture.RepairStep{Control: step.Control, Status: status, ReasonCode: reason})
	}
	switch envelope.Status {
	case "success", "failed":
		return guardposture.RepairResult{Outcome: document.Outcome, Steps: steps}, nil
	case "denied":
		code := "refused"
		for _, step := range steps {
			if step.Status == "blocked" && step.ReasonCode != "" {
				code = step.ReasonCode
				break
			}
		}
		return guardposture.RepairResult{}, &guardposture.RepairRefusal{Code: code, Steps: steps}
	default:
		return guardposture.RepairResult{}, fmt.Errorf("host security repair status %q is unknown", envelope.Status)
	}
}
