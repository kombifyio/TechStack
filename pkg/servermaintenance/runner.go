package servermaintenance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

// ServerReader is the canonical server read the reboot watcher needs.
type ServerReader interface {
	GetServerRuntime(ctx context.Context, tenantID, serverID string) (*controlplane.ServerRuntime, error)
}

// Runner advances one durable maintenance job by one step. Every write is a
// compare-and-set on the job state, so two runners never both dispatch.
//
// Loop selects the active jobs and calls Advance; jobs.HostMaintenanceDispatcher
// implements Dispatcher. A queued job is only created once the route sees a
// wired Dispatcher and an agent advertising AgentCapability.
type Runner struct {
	Jobs       controlplane.ServerMaintenanceStore
	Servers    ServerReader
	Dispatcher Dispatcher
	// Events receives the server timeline events of a reboot or OS update
	// (reboot_acknowledged, reboot_observed, os_update_applied and failures).
	// Nil records nothing.
	Events controlplane.ServerEventStore
	Now    func() time.Time
}

func (r Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// Advance runs the next step of job and returns the stored result. A queued,
// running or waiting job past its deadline fails with deadline_exceeded, which
// releases the fence. A running job whose command was already sent is
// re-attached to that command's durable outcome, never failed because the
// process that sent it stopped waiting. A reboot awaiting its node is decided
// by the watcher, and an apply that outlived the CLI's wait is polled.
func (r Runner) Advance(ctx context.Context, job controlplane.ServerMaintenanceJob) (*controlplane.ServerMaintenanceJob, error) {
	next, err := r.advance(ctx, job)
	if err == nil && next != nil && next.State != job.State {
		writeCtx, cancel := detachedWriteContext(ctx)
		r.recordTimeline(writeCtx, *next)
		cancel()
	}
	return next, err
}

// NeedsDispatchSlot reports whether the next step of job talks to an agent
// and may block for minutes. Watching a reboot, expiring a job and skipping a
// poll that is not due are cheap and never wait for a slot.
func (r Runner) NeedsDispatchSlot(job controlplane.ServerMaintenanceJob) bool {
	if r.overdue(job) {
		return false
	}
	switch job.State {
	case controlplane.ServerMaintenanceStateQueued:
		return true
	case controlplane.ServerMaintenanceStateWaiting:
		return !r.now().Before(job.UpdatedAt.UTC().Add(ApplyPollInterval))
	case controlplane.ServerMaintenanceStateRunning:
		return stringValue(job.Result, ResultCommandID) != ""
	default:
		return false
	}
}

func (r Runner) advance(ctx context.Context, job controlplane.ServerMaintenanceJob) (*controlplane.ServerMaintenanceJob, error) {
	switch job.State {
	case controlplane.ServerMaintenanceStateQueued:
		if r.overdue(job) {
			return r.expire(ctx, job)
		}
		return r.dispatch(ctx, job)
	case controlplane.ServerMaintenanceStateAwaitingNodeReturn:
		return r.watchReboot(ctx, job)
	case controlplane.ServerMaintenanceStateWaiting:
		if r.overdue(job) {
			return r.expire(ctx, job)
		}
		if r.now().Before(job.UpdatedAt.UTC().Add(ApplyPollInterval)) {
			return &job, nil
		}
		return r.pollApply(ctx, job)
	case controlplane.ServerMaintenanceStateRunning:
		return r.reattach(ctx, job)
	default:
		return &job, nil
	}
}

func (r Runner) overdue(job controlplane.ServerMaintenanceJob) bool {
	return job.DeadlineAt != nil && !r.now().Before(job.DeadlineAt.UTC())
}

const resultInFlightGrace = "in_flight_expiry_grace"

func cloneResult(values map[string]any) map[string]any {
	result := make(map[string]any, len(values)+1)
	for key, value := range values {
		result[key] = value
	}
	return result
}

// expire fails an overdue job and releases its node. A recorded command is
// withdrawn first: one still queued (for example sent just before a crash)
// must not reach the agent after the node is released. If the withdrawal
// cannot be confirmed the job keeps holding the node and expiry is retried;
// if an agent already received the command without reporting an outcome, the
// job's deadline moves once by InFlightExpiryGrace.
func (r Runner) expire(ctx context.Context, job controlplane.ServerMaintenanceJob) (*controlplane.ServerMaintenanceJob, error) {
	if target := r.target(job); target.CommandID != "" {
		if withdrawer, ok := r.Dispatcher.(CommandWithdrawer); ok {
			withdrawCtx, cancel := detachedWriteContext(ctx)
			withdrawal, err := withdrawer.Withdraw(withdrawCtx, target)
			cancel()
			if err != nil {
				return nil, fmt.Errorf("withdraw maintenance command before expiry: %w", err)
			}
			if withdrawal.InFlight && job.Result[resultInFlightGrace] != true {
				// Extend the deadline once rather than hold silently: the
				// node's hold (deadline_at) must cover the extra wait too.
				result := cloneResult(job.Result)
				result[resultInFlightGrace] = true
				deadline := r.now().Add(InFlightExpiryGrace)
				return r.write(ctx, job.TenantID, job.ID, controlplane.ServerMaintenanceUpdate{
					ExpectedState: job.State, State: job.State, ReasonCode: job.ReasonCode,
					DeadlineAt: &deadline, Result: result, At: r.now(),
				})
			}
		}
	}
	return r.write(ctx, job.TenantID, job.ID, controlplane.ServerMaintenanceUpdate{
		ExpectedState: job.State, State: controlplane.ServerMaintenanceStateFailed,
		ReasonCode: ReasonDeadlineExceeded, DeadlineAt: job.DeadlineAt, Result: job.Result, At: r.now(),
	})
}

// write stores a transition on a context detached from the caller: a
// shutdown or a timed-out command must not drop the terminal write that
// releases the node.
func (r Runner) write(ctx context.Context, tenantID, jobID string, update controlplane.ServerMaintenanceUpdate) (*controlplane.ServerMaintenanceJob, error) {
	writeCtx, cancel := detachedWriteContext(ctx)
	defer cancel()
	return r.Jobs.UpdateServerMaintenanceJob(writeCtx, tenantID, jobID, update)
}

func detachedWriteContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
}

// interrupted reports a dispatch that ended because this process stopped
// waiting, not because the host answered. The command may still run.
func interrupted(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrReattachUnsupported)
}

// claim moves a queued job to running and records the command id it is about
// to send. The store re-checks the node in the same transaction under the
// node's advisory lock: stack or service work that reached the node by any
// path leaves the job queued (nil claimed, nil error) until the node frees up
// or the job expires. This is the authoritative guard against running
// maintenance next to node work.
func (r Runner) claim(ctx context.Context, job controlplane.ServerMaintenanceJob, commandID string) (*controlplane.ServerMaintenanceJob, error) {
	now := r.now()
	claimed, err := r.Jobs.ClaimServerMaintenanceJob(ctx, job.TenantID, job.ID, commandID, now.Add(ExecutionTimeout(job.Action)), now)
	var busy *controlplane.NodeBusyError
	if errors.As(err, &busy) {
		return nil, nil
	}
	return claimed, err
}

func (r Runner) target(job controlplane.ServerMaintenanceJob) Target {
	return Target{
		TenantID: job.TenantID, ServerID: job.ServerID, AgentID: job.AgentID, JobID: job.ID,
		CommandID: stringValue(job.Result, ResultCommandID),
	}
}

func (r Runner) dispatch(ctx context.Context, job controlplane.ServerMaintenanceJob) (*controlplane.ServerMaintenanceJob, error) {
	if r.Dispatcher == nil {
		return nil, ErrDispatchUnavailable
	}
	claimed, err := r.claim(ctx, job, CommandID(job.ID, job.Action, r.now()))
	if err != nil {
		return nil, err
	}
	if claimed == nil {
		return &job, nil
	}
	return r.execute(ctx, *claimed, r.target(*claimed))
}

// reattach waits for the outcome of the command a running job already sent,
// bounded by the job deadline. A job without a recorded command sent nothing
// and only expires. Past the deadline one short look at the outcome precedes
// expiry, so a late acknowledgement is not reported as a failure.
func (r Runner) reattach(ctx context.Context, job controlplane.ServerMaintenanceJob) (*controlplane.ServerMaintenanceJob, error) {
	target := r.target(job)
	if target.CommandID == "" || r.Dispatcher == nil {
		if r.overdue(job) {
			return r.expire(ctx, job)
		}
		return &job, nil
	}
	target.Reattach = true
	var stepCtx context.Context
	var cancel context.CancelFunc
	switch {
	case r.overdue(job):
		stepCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
	case job.DeadlineAt != nil:
		stepCtx, cancel = context.WithDeadline(ctx, job.DeadlineAt.UTC())
	default:
		stepCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	next, err := r.execute(stepCtx, job, target)
	if err == nil && next != nil && next.State == controlplane.ServerMaintenanceStateRunning && r.overdue(job) {
		return r.expire(ctx, job)
	}
	return next, err
}

// execute runs job's command (or re-attaches to it) and stores the outcome.
func (r Runner) execute(ctx context.Context, job controlplane.ServerMaintenanceJob, target Target) (*controlplane.ServerMaintenanceJob, error) {
	next := controlplane.ServerMaintenanceUpdate{ExpectedState: controlplane.ServerMaintenanceStateRunning, DeadlineAt: job.DeadlineAt}
	result := map[string]any{ResultCommandID: target.CommandID}
	var err error
	switch job.Action {
	case controlplane.ServerMaintenanceActionPlan:
		var plan PlanResult
		if plan, err = r.Dispatcher.Plan(ctx, target); err == nil {
			next.State, next.PlanDigest = controlplane.ServerMaintenanceStateCompleted, plan.PlanDigest
			for key, value := range PlanResultDocument(plan) {
				result[key] = value
			}
		}
	case controlplane.ServerMaintenanceActionUpdate:
		var applied ApplyResult
		if applied, err = r.Dispatcher.Apply(ctx, target, job.PlanDigest); err == nil {
			result["outcome"], result["reboot_required"], result["unit"], result["changed"] = applied.Outcome, applied.RebootRequired, applied.Unit, applied.Changed
			switch applied.Outcome {
			case "applied", "noop":
				next.State = controlplane.ServerMaintenanceStateCompleted
			case "running":
				// The install unit outlived the CLI's wait: poll it and keep the
				// job alive while the unit is observed running.
				next.State, next.ReasonCode = controlplane.ServerMaintenanceStateWaiting, ReasonApplyStillRunning
				next.DeadlineAt = r.extendedApplyDeadline(job)
			default:
				next.State, next.ReasonCode = controlplane.ServerMaintenanceStateFailed, SafeReasonCode(applied.FailureCode, "install_failed")
			}
		}
	case controlplane.ServerMaintenanceActionReboot:
		var rebooted RebootResult
		if rebooted, err = r.Dispatcher.Reboot(ctx, target, RebootDelay); err == nil {
			deadline := rebooted.ScheduledAt.UTC().Add(GuardHorizon + ReturnWindow)
			next.State, next.DeadlineAt = controlplane.ServerMaintenanceStateAwaitingNodeReturn, &deadline
			result["boot_id_before"], result["scheduled_at"] = rebooted.BootIDBefore, rebooted.ScheduledAt.UTC().Format(time.RFC3339Nano)
		}
	default:
		err = errors.New("unsupported maintenance action")
	}
	switch {
	case errors.Is(err, ErrCommandNotDelivered):
		return r.write(ctx, job.TenantID, job.ID, controlplane.ServerMaintenanceUpdate{
			ExpectedState: controlplane.ServerMaintenanceStateRunning, State: controlplane.ServerMaintenanceStateFailed,
			ReasonCode: ReasonDispatchInterrupted, Result: result, At: r.now(),
		})
	case err != nil && interrupted(ctx, err):
		// The command may be running on the host: keep the job running, and
		// the next step re-attaches to its durable outcome.
		return &job, nil
	case err != nil:
		return r.fail(ctx, &job, err, result)
	}
	next.Result, next.At = result, r.now()
	return r.write(ctx, job.TenantID, job.ID, next)
}

func (r Runner) fail(ctx context.Context, job *controlplane.ServerMaintenanceJob, cause error, result map[string]any) (*controlplane.ServerMaintenanceJob, error) {
	reason := ReasonDispatchFailed
	var refusal *Refusal
	var failure *Failure
	switch {
	case errors.As(cause, &refusal):
		reason = SafeReasonCode(refusal.Code, ReasonHostCommandFailed)
	case errors.As(cause, &failure):
		reason = SafeReasonCode(failure.Code, ReasonHostCommandFailed)
	}
	return r.write(ctx, job.TenantID, job.ID, controlplane.ServerMaintenanceUpdate{
		ExpectedState: job.State, State: controlplane.ServerMaintenanceStateFailed, ReasonCode: reason, Result: result, At: r.now(),
	})
}

// pollApply recovers a StackKits exit-4 outcome with read-only plans. Command
// admission requires a release whose Plan observes active update units; a
// busy refusal keeps the job alive without sending another install. A settled
// plan confirms the package outcome, while dpkg problems mean it is settling.
func (r Runner) pollApply(ctx context.Context, job controlplane.ServerMaintenanceJob) (*controlplane.ServerMaintenanceJob, error) {
	target := Target{TenantID: job.TenantID, ServerID: job.ServerID, AgentID: job.AgentID, JobID: job.ID}
	now := r.now()
	still := func(extend bool) (*controlplane.ServerMaintenanceJob, error) {
		update := controlplane.ServerMaintenanceUpdate{
			ExpectedState: controlplane.ServerMaintenanceStateWaiting, State: controlplane.ServerMaintenanceStateWaiting,
			ReasonCode: ReasonApplyStillRunning, DeadlineAt: job.DeadlineAt, At: now,
		}
		if extend {
			update.DeadlineAt = r.extendedApplyDeadline(job)
		}
		return r.write(ctx, job.TenantID, job.ID, update)
	}
	plan, err := r.Dispatcher.Plan(ctx, target)
	var refusal *Refusal
	switch {
	case errors.As(err, &refusal) && refusal.Code == RefusalPackageManagerBusy:
		return still(true)
	case errors.As(err, &refusal) && refusal.Code == RefusalStackKitsUpgradeRequired:
		// A previously admitted Apply may still run on the older release.
		// Refusing an unsupported observation proves neither progress nor
		// completion: keep its fence under the unchanged deadline.
		return still(false)
	case errors.As(err, &refusal):
		return r.fail(ctx, &job, err, job.Result)
	case err != nil:
		// Not proof of progress (the agent may be briefly unreachable): poll
		// again later under the unchanged deadline.
		return still(false)
	case len(plan.DpkgProblems) > 0:
		return still(true)
	}
	result := map[string]any{}
	for key, value := range job.Result {
		result[key] = value
	}
	result["outcome"] = "applied"
	result["remaining_pending_count"] = plan.PendingCount
	result["plan_digest_after"] = plan.PlanDigest
	result["reboot_likely"] = plan.RebootLikely
	state, reason := controlplane.ServerMaintenanceStateCompleted, ReasonApplyFinishedAfterWait
	// A settled plan alone does not prove the approved packages installed. A
	// package of the approved set still pending at its approved starting
	// version was not installed, so the update is partial, not applied.
	if notInstalled := r.notInstalledPackages(ctx, job, plan.Packages); len(notInstalled) > 0 {
		state, reason = controlplane.ServerMaintenanceStateFailed, ReasonApplyPartial
		result["outcome"] = "partial"
		result["not_installed"] = notInstalled
	}
	return r.write(ctx, job.TenantID, job.ID, controlplane.ServerMaintenanceUpdate{
		ExpectedState: controlplane.ServerMaintenanceStateWaiting, State: state,
		ReasonCode: reason, Result: result, DeadlineAt: job.DeadlineAt, At: now,
	})
}

// notInstalledPackages returns the approved packages that are still pending
// with the installed version they had when the owner approved the plan,
// bounded to MaxStoredPlanHeld names. The approved set is the stored plan
// the update was admitted against; without it nothing can be compared.
func (r Runner) notInstalledPackages(ctx context.Context, job controlplane.ServerMaintenanceJob, pending []PlanPackage) []any {
	if len(pending) == 0 || strings.TrimSpace(job.PlanDigest) == "" {
		return nil
	}
	approvedPlan, err := r.Jobs.LatestServerMaintenancePlan(ctx, job.TenantID, job.ServerID, job.PlanDigest)
	if err != nil || approvedPlan == nil {
		return nil
	}
	approved := make(map[string]string)
	listed, _ := approvedPlan.Result["packages"].([]any)
	for _, raw := range listed {
		entry, _ := raw.(map[string]any)
		if name := stringValue(entry, "name"); name != "" {
			approved[name] = stringValue(entry, "from")
		}
	}
	out := make([]any, 0)
	for _, pkg := range pending {
		from, ok := approved[strings.TrimSpace(pkg.Name)]
		if !ok || from != strings.TrimSpace(pkg.From) {
			continue
		}
		out = append(out, boundedPlanString(pkg.Name))
		if len(out) == MaxStoredPlanHeld {
			break
		}
	}
	return out
}

// extendedApplyDeadline moves a waiting apply's deadline forward while its
// unit is observed running, never past MaxApplyDuration after the request.
func (r Runner) extendedApplyDeadline(job controlplane.ServerMaintenanceJob) *time.Time {
	deadline := r.now().Add(ApplyStillRunningExtension)
	if limit := job.CreatedAt.UTC().Add(MaxApplyDuration); !job.CreatedAt.IsZero() && deadline.After(limit) {
		deadline = limit
	}
	return &deadline
}

func (r Runner) watchReboot(ctx context.Context, job controlplane.ServerMaintenanceJob) (*controlplane.ServerMaintenanceJob, error) {
	server, err := r.Servers.GetServerRuntime(ctx, job.TenantID, job.ServerID)
	if errors.Is(err, controlplane.ErrNotFound) {
		// The server was removed while it rebooted: nothing can return, so the
		// job fails and frees the fence.
		return r.write(ctx, job.TenantID, job.ID, controlplane.ServerMaintenanceUpdate{
			ExpectedState: controlplane.ServerMaintenanceStateAwaitingNodeReturn, State: controlplane.ServerMaintenanceStateFailed,
			ReasonCode: ReasonServerRemoved, DeadlineAt: job.DeadlineAt, At: r.now(),
		})
	}
	if err != nil {
		return nil, err
	}
	scheduledAt, _ := time.Parse(time.RFC3339Nano, stringValue(job.Result, "scheduled_at"))
	connected := serverregistry.MutationsAllowed(server.ConnectionState)
	observation := RebootObservation{
		BootIDBefore: stringValue(job.Result, "boot_id_before"), ScheduledAt: scheduledAt,
		BootID: stringValue(server.Metadata, MetadataBootID), Connected: connected, Now: r.now(),
	}
	if !connected {
		observation.OfflineSince = server.ConnectionChangedAt
	}
	verdict := DecideReboot(observation)
	if !verdict.Done {
		return &job, nil
	}
	state := controlplane.ServerMaintenanceStateFailed
	if verdict.Succeeded {
		state = controlplane.ServerMaintenanceStateCompleted
	}
	return r.write(ctx, job.TenantID, job.ID, controlplane.ServerMaintenanceUpdate{
		ExpectedState: controlplane.ServerMaintenanceStateAwaitingNodeReturn, State: state,
		ReasonCode: verdict.ReasonCode, DeadlineAt: job.DeadlineAt, At: observation.Now,
	})
}

func stringValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
