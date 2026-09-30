package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/agentcontrol"
	agentpb "github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/servermaintenance"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// Host maintenance command budgets. StackKits bounds plan to 3 minutes and
// the apply wait to 20 minutes; the agent gets headroom on top of both.
const (
	hostPlanCommandTimeout   = 5 * time.Minute
	hostApplyCommandTimeout  = 25 * time.Minute
	hostRebootCommandTimeout = 2 * time.Minute
	hostCommandResultGrace   = time.Minute
	hostMaintenanceSchema    = "stackkit.host-maintenance/v1"
)

// HostMaintenanceDispatcher runs the StackKits host maintenance commands on a
// node's agent through the typed StackKit command channel. The hub refuses
// an agent that does not advertise stackkit.host-maintenance.v1.
type HostMaintenanceDispatcher struct {
	sender          StackKitCommandSender
	releaseResolver func() (*stackkitrelease.Release, error)
	now             func() time.Time
}

// NewHostMaintenanceDispatcher returns nil without a sender, which keeps
// server maintenance inert.
func NewHostMaintenanceDispatcher(sender StackKitCommandSender) *HostMaintenanceDispatcher {
	if sender == nil {
		return nil
	}
	return &HostMaintenanceDispatcher{sender: sender, releaseResolver: configuredTargetStackKitRelease, now: time.Now}
}

var _ servermaintenance.Dispatcher = (*HostMaintenanceDispatcher)(nil)

// HostMaintenanceDispatcherFor returns the dispatcher as the interface, or a
// nil interface without a sender, so callers can test it against nil.
func HostMaintenanceDispatcherFor(sender StackKitCommandSender) servermaintenance.Dispatcher {
	if dispatcher := NewHostMaintenanceDispatcher(sender); dispatcher != nil {
		return dispatcher
	}
	return nil
}

func (d *HostMaintenanceDispatcher) Plan(ctx context.Context, target servermaintenance.Target) (servermaintenance.PlanResult, error) {
	data, _, err := d.run(ctx, target, agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_UPDATE_PLAN, "", hostPlanCommandTimeout)
	if err != nil {
		return servermaintenance.PlanResult{}, err
	}
	if !stackkitcommand.ValidHostPlanDigest(data.PlanDigest) {
		return servermaintenance.PlanResult{}, errors.New("host update plan returned no valid plan digest")
	}
	held := make([]string, 0, len(data.Held))
	for _, pkg := range data.Held {
		held = append(held, pkg.Name)
	}
	packages := make([]servermaintenance.PlanPackage, 0, len(data.Packages))
	for _, pkg := range data.Packages {
		packages = append(packages, servermaintenance.PlanPackage{Name: pkg.Name, From: pkg.From, To: pkg.To, Security: pkg.Security})
	}
	return servermaintenance.PlanResult{
		PlanDigest: data.PlanDigest, PendingCount: data.PendingCount, SecurityCount: data.SecurityCount,
		RebootLikely: data.RebootLikely, Held: held, HoldScope: data.HoldScope,
		Packages: packages, DpkgProblems: data.DpkgProblems,
	}, nil
}

func (d *HostMaintenanceDispatcher) Apply(ctx context.Context, target servermaintenance.Target, planDigest string) (servermaintenance.ApplyResult, error) {
	if !stackkitcommand.ValidHostPlanDigest(planDigest) {
		return servermaintenance.ApplyResult{}, errors.New("host update apply requires a sha256 plan digest")
	}
	data, exitCode, err := d.run(ctx, target, agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_UPDATE_APPLY, planDigest, hostApplyCommandTimeout)
	var failure *servermaintenance.Failure
	switch {
	case err == nil:
		return servermaintenance.ApplyResult{
			Outcome: data.Outcome, RebootRequired: data.RebootRequired, Unit: data.Unit, Changed: len(data.Changes),
		}, nil
	case errors.As(err, &failure) && (exitCode == 4 || data.Outcome == "running"):
		// Exit 4: the CLI stopped waiting, the install unit keeps running.
		return servermaintenance.ApplyResult{Outcome: "running", Unit: data.Unit, FailureCode: failure.Code}, nil
	case errors.As(err, &failure):
		return servermaintenance.ApplyResult{
			Outcome: "failed", FailureCode: failure.Code, RebootRequired: data.RebootRequired, Unit: data.Unit, Changed: len(data.Changes),
		}, nil
	default:
		return servermaintenance.ApplyResult{}, err
	}
}

func (d *HostMaintenanceDispatcher) Reboot(ctx context.Context, target servermaintenance.Target, _ time.Duration) (servermaintenance.RebootResult, error) {
	data, _, err := d.run(ctx, target, agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_REBOOT, "", hostRebootCommandTimeout)
	if err != nil {
		return servermaintenance.RebootResult{}, err
	}
	scheduledAt, parseErr := time.Parse(time.RFC3339Nano, data.ScheduledAt)
	if parseErr != nil || strings.TrimSpace(data.BootIDBefore) == "" {
		return servermaintenance.RebootResult{}, errors.New("host reboot returned no boot id or schedule")
	}
	return servermaintenance.RebootResult{BootIDBefore: strings.TrimSpace(data.BootIDBefore), ScheduledAt: scheduledAt.UTC()}, nil
}

// hostMaintenanceData is the stackkit.host-maintenance/v1 document.
type hostMaintenanceData struct {
	SchemaVersion  string                   `json:"schema_version"`
	PlanDigest     string                   `json:"plan_digest"`
	PendingCount   int                      `json:"pending_count"`
	SecurityCount  int                      `json:"security_count"`
	Held           []hostMaintenancePackage `json:"held"`
	HoldScope      string                   `json:"hold_scope"`
	Packages       []hostMaintenancePackage `json:"packages"`
	DpkgProblems   []string                 `json:"dpkg_problems"`
	RebootLikely   bool                     `json:"reboot_likely"`
	Outcome        string                   `json:"outcome"`
	Unit           string                   `json:"unit"`
	Changes        []json.RawMessage        `json:"changes"`
	RebootRequired bool                     `json:"reboot_required"`
	BootIDBefore   string                   `json:"boot_id_before"`
	ScheduledAt    string                   `json:"scheduled_at"`
	Refusal        *hostMaintenanceProblem  `json:"refusal"`
	Failure        *hostMaintenanceProblem  `json:"failure"`
}

type hostMaintenancePackage struct {
	Name     string `json:"name"`
	From     string `json:"from"`
	To       string `json:"to"`
	Security bool   `json:"security"`
}

type hostMaintenanceProblem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// run sends one host command and returns its host-maintenance document. A
// StackKits refusal (exit 3) is a *servermaintenance.Refusal and a failure
// (exit 1 or 4) a *servermaintenance.Failure; the document is returned with
// both, because a failed apply still reports its unit and changes.
func (d *HostMaintenanceDispatcher) run(ctx context.Context, target servermaintenance.Target, operation agentpb.StackKitOperation, planDigest string, timeout time.Duration) (hostMaintenanceData, int32, error) {
	if d == nil || d.sender == nil {
		return hostMaintenanceData{}, 0, servermaintenance.ErrDispatchUnavailable
	}
	release, err := d.releaseResolver()
	if err != nil {
		return hostMaintenanceData{}, 0, fmt.Errorf("resolve pinned StackKits release: %w", err)
	}
	if release == nil {
		return hostMaintenanceData{}, 0, errors.New("pinned published StackKits release is not configured")
	}
	commandID := strings.TrimSpace(target.CommandID)
	if commandID == "" {
		commandID = hostMaintenanceCommandID(target.JobID, operation, d.now())
	}
	var result *agentpb.StackKitResult
	if target.Reattach {
		result, err = d.reattach(ctx, target.TenantID, commandID)
	} else {
		if operation != agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_REBOOT &&
			!stackkitcommand.ReleaseAtLeast(release.Receipt().Version, stackkitcommand.MinHostUpdateRelease) {
			return hostMaintenanceData{}, 0, &servermaintenance.Refusal{
				Code: servermaintenance.RefusalStackKitsUpgradeRequired,
				Message: "Host updates require StackKits " + stackkitcommand.MinHostUpdateRelease +
					" or newer. Upgrade the configured release pin or served Linux runtime bundle before planning or applying.",
			}
		}
		command := &agentpb.StackKitCommand{
			CommandId:      commandID,
			Operation:      operation,
			TimeoutSeconds: int32(timeout / time.Second),
			Release:        grpcserver.StackKitReleasePinFor(*release),
			OwnerApproved:  operation != agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_UPDATE_PLAN,
			HostPlanDigest: planDigest,
		}
		if err := stackkitcommand.ValidateCommand(command); err != nil {
			return hostMaintenanceData{}, 0, err
		}
		result, err = sendStackKitCommandBoundedWithGraceForTenant(ctx, d.sender, target.TenantID, target.AgentID, command, hostCommandResultGrace)
	}
	if errors.Is(err, agentcontrol.ErrCommandCancelledBeforeDispatch) {
		return hostMaintenanceData{}, 0, fmt.Errorf("%w: %v", servermaintenance.ErrCommandNotDelivered, err)
	}
	if err != nil {
		return hostMaintenanceData{}, 0, err
	}
	if result == nil {
		return hostMaintenanceData{}, 0, errors.New("host maintenance command returned no result")
	}
	return parseHostMaintenanceResult(result)
}

// stackKitCommandAwaiter is the durable command channel that can wait for
// the outcome of a command sent earlier (the HTTPS agent hub).
type stackKitCommandAwaiter interface {
	AwaitStackKitCommandForTenant(ctx context.Context, tenantID, commandID string) (*agentpb.StackKitResult, error)
}

func (d *HostMaintenanceDispatcher) reattach(ctx context.Context, tenantID, commandID string) (*agentpb.StackKitResult, error) {
	awaiter, ok := d.sender.(stackKitCommandAwaiter)
	if !ok {
		return nil, servermaintenance.ErrReattachUnsupported
	}
	return awaiter.AwaitStackKitCommandForTenant(ctx, tenantID, commandID)
}

// stackKitCommandWithdrawer is the durable command channel that can fail a
// command no agent has received yet (the HTTPS agent hub).
type stackKitCommandWithdrawer interface {
	WithdrawQueuedStackKitCommandForTenant(ctx context.Context, tenantID, commandID string) (agentcontrol.CommandWithdrawal, error)
}

var _ servermaintenance.CommandWithdrawer = (*HostMaintenanceDispatcher)(nil)

// Withdraw fails the job's recorded command if no agent has received it. A
// sender without a durable queue loses undelivered commands with its process,
// so there is nothing to withdraw.
func (d *HostMaintenanceDispatcher) Withdraw(ctx context.Context, target servermaintenance.Target) (servermaintenance.Withdrawal, error) {
	if d == nil || strings.TrimSpace(target.CommandID) == "" {
		return servermaintenance.Withdrawal{}, nil
	}
	withdrawer, ok := d.sender.(stackKitCommandWithdrawer)
	if !ok {
		return servermaintenance.Withdrawal{}, nil
	}
	outcome, err := withdrawer.WithdrawQueuedStackKitCommandForTenant(ctx, target.TenantID, target.CommandID)
	if err != nil {
		return servermaintenance.Withdrawal{}, err
	}
	return servermaintenance.Withdrawal{Withdrawn: outcome.Withdrawn, InFlight: outcome.InFlight}, nil
}

func parseHostMaintenanceResult(result *agentpb.StackKitResult) (hostMaintenanceData, int32, error) {
	var envelope struct {
		Status string          `json:"status"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(result.CommandResultJson, &envelope); err != nil {
		return hostMaintenanceData{}, result.ExitCode, fmt.Errorf("decode host maintenance result: %w", err)
	}
	var data hostMaintenanceData
	if json.Unmarshal(envelope.Data, &data) != nil || data.SchemaVersion != hostMaintenanceSchema {
		// The agent wraps output it could not type as data.output; a host
		// command that printed nothing typed is a dispatch failure.
		return hostMaintenanceData{}, result.ExitCode, fmt.Errorf("host maintenance command returned no %s document (exit %d)", hostMaintenanceSchema, result.ExitCode)
	}
	switch envelope.Status {
	case "success":
		if !result.Success {
			return data, result.ExitCode, errors.New("host maintenance result conflicts with its transport status")
		}
		return data, result.ExitCode, nil
	case "denied":
		refusal := &servermaintenance.Refusal{Code: "host_refused"}
		if data.Refusal != nil {
			refusal.Code = servermaintenance.SafeReasonCode(data.Refusal.Code, servermaintenance.ReasonHostCommandFailed)
			refusal.Message = data.Refusal.Message
		}
		return data, result.ExitCode, refusal
	default:
		failure := &servermaintenance.Failure{Code: servermaintenance.ReasonHostCommandFailed}
		if data.Failure != nil {
			failure.Code = servermaintenance.SafeReasonCode(data.Failure.Code, servermaintenance.ReasonHostCommandFailed)
			failure.Message = data.Failure.Message
		}
		return data, result.ExitCode, failure
	}
}

// hostMaintenanceCommandID is unique per attempt: a waiting apply polls
// several times under one job.
func hostMaintenanceCommandID(jobID string, operation agentpb.StackKitOperation, now time.Time) string {
	action := map[agentpb.StackKitOperation]string{
		agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_UPDATE_PLAN:  controlplane.ServerMaintenanceActionPlan,
		agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_UPDATE_APPLY: controlplane.ServerMaintenanceActionUpdate,
		agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_REBOOT:       controlplane.ServerMaintenanceActionReboot,
	}[operation]
	return servermaintenance.CommandID(jobID, action, now)
}
