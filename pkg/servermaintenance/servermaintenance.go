// Package servermaintenance owns the node-maintenance contract between the
// control plane and the agent: the capability an agent must advertise, the
// heartbeat facts the control plane reads, the dispatch seam onto
// `stackkit host updates plan|apply` and `stackkit host reboot`
// (StackKits schema stackkit.host-maintenance/v1), and the job runner that
// advances a durable maintenance job.
//
// jobs.HostMaintenanceDispatcher implements the dispatch seam over the typed
// StackKit command channel; without it the route never advertises or admits
// a maintenance action.
package servermaintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// AgentCapability is advertised by an agent that can run the StackKits host
// maintenance commands. Without it no maintenance action is offered.
const AgentCapability = stackkitcommand.HostMaintenanceCapability

// Server metadata keys an agent heartbeat reports. Both are optional: an
// absent value is "unknown", never a match.
const (
	// MetadataMachineIDDigest is MachineIDDigest of the node's /etc/machine-id.
	MetadataMachineIDDigest = "host_machine_id_sha256"
	// MetadataBootID is /proc/sys/kernel/random/boot_id; a new value after a
	// reboot request proves that the node rebooted.
	MetadataBootID = "host_boot_id"
	// MetadataHostMaintenance is true while the node's agent advertises
	// AgentCapability (HTTPS agents report it with every heartbeat).
	MetadataHostMaintenance = "host_maintenance"
)

// Techstack maintenance policy (owner decisions 2026-09-25).
const (
	// PlanMaxAge bounds how old the plan behind an os_update may be.
	PlanMaxAge = 15 * time.Minute
	// RebootDelay is the delay passed to `stackkit host reboot --delay`.
	RebootDelay = 30 * time.Second
	// ReturnWindow is how long a node that went offline for a reboot has to
	// come back with a new boot id.
	ReturnWindow = 10 * time.Minute
	// GuardHorizon is how long the StackKits reboot guard may wait for package
	// operations after the timer fires before it abandons the reboot.
	GuardHorizon = 15 * time.Minute
	// QueueTimeout bounds how long a job may wait queued, for example behind
	// stack or service work on the node, before it expires and frees the fence.
	QueueTimeout = 15 * time.Minute
	// ApplyPollInterval is how often a waiting apply is re-planned.
	ApplyPollInterval = time.Minute
	// ApplyStillRunningExtension is how far a busy re-plan (the install unit
	// is still running) moves a waiting apply's deadline.
	ApplyStillRunningExtension = 20 * time.Minute
	// MaxApplyDuration caps an OS update, however long its unit reports
	// progress.
	MaxApplyDuration = 3 * time.Hour
	// InFlightExpiryGrace is how long past its deadline a job keeps holding
	// the node while an agent has received its command but reported no
	// outcome; the command may still be running on the host.
	InFlightExpiryGrace = 30 * time.Minute
)

// ExecutionTimeout bounds how long a claimed job may run before the runner
// expires it: a runner that crashes mid-dispatch never holds the fence
// forever. A dispatched reboot replaces it with its own return deadline.
func ExecutionTimeout(action string) time.Duration {
	switch action {
	case controlplane.ServerMaintenanceActionUpdate:
		return time.Hour
	case controlplane.ServerMaintenanceActionReboot:
		return 5 * time.Minute
	default:
		return 15 * time.Minute
	}
}

// Stable job reason codes for outcomes the runner records.
const (
	ReasonNodeReturned      = "node_returned"
	ReasonNodeDidNotReturn  = "node_did_not_return"
	ReasonRebootAbandoned   = "reboot_abandoned"
	ReasonDispatchFailed    = "dispatch_failed"
	ReasonApplyStillRunning = "apply_still_running"
	ReasonDeadlineExceeded  = "deadline_exceeded"
	ReasonServerRemoved     = "server_removed"
	// ReasonApplyFinishedAfterWait completes an apply whose unit finished
	// after the CLI stopped waiting.
	ReasonApplyFinishedAfterWait = "apply_finished_after_wait"
	// ReasonApplyPartial fails an apply whose unit ended while packages of the
	// approved plan are still pending at their approved starting versions.
	ReasonApplyPartial = "apply_partial"
	// ReasonDispatchInterrupted fails a job whose command ended before an
	// agent received it; nothing ran on the host.
	ReasonDispatchInterrupted = "dispatch_interrupted"
	// ReasonHostCommandFailed replaces a host code that is not a bounded token.
	ReasonHostCommandFailed = "host_command_failed"
	// RefusalPlanStale is the StackKits refusal of an apply whose plan no
	// longer matches the host.
	RefusalPlanStale = "plan_stale"
)

const machineIDDigestDomain = "kombify-techstack-host-identity/v1\x00"

// MachineIDDigest is the domain-separated SHA-256 of a raw machine id, so the
// id itself never leaves the host. The control plane and the agent must use
// this exact function. An empty id yields "".
func MachineIDDigest(machineID string) string {
	machineID = strings.TrimSpace(machineID)
	if machineID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(machineIDDigestDomain + machineID))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// LocalMachineIDDigest reads the host's own /etc/machine-id. It returns ""
// when the file is unreadable: the control-plane check then treats every node
// as unknown and the node-side reboot guard remains the backstop.
func LocalMachineIDDigest(path string) string {
	if strings.TrimSpace(path) == "" {
		path = "/etc/machine-id"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return MachineIDDigest(string(raw))
}

// Target is the node a dispatch addresses. CommandID is the typed agent
// command id the job recorded before it sent the command; with Reattach the
// dispatcher sends nothing and waits for that command's durable outcome, so a
// restarted control plane picks up a reboot or update it had already sent.
type Target struct {
	TenantID  string
	ServerID  string
	AgentID   string
	JobID     string
	CommandID string
	Reattach  bool
}

// ResultCommandID is the job result key that records the typed command id.
const ResultCommandID = "command_id"

// CommandID is the typed agent command id of one attempt of action for jobID.
func CommandID(jobID, action string, now time.Time) string {
	short := map[string]string{
		controlplane.ServerMaintenanceActionPlan:   "plan",
		controlplane.ServerMaintenanceActionUpdate: "apply",
		controlplane.ServerMaintenanceActionReboot: "reboot",
	}[action]
	return strings.TrimSpace(jobID) + ":" + short + ":" + strconv.FormatInt(now.UTC().UnixNano(), 36)
}

// ErrCommandNotDelivered reports a command that ended before any agent
// received it: nothing ran on the host.
var ErrCommandNotDelivered = errors.New("servermaintenance: host command ended before an agent received it")

// ErrReattachUnsupported reports a command channel without durable outcomes.
var ErrReattachUnsupported = errors.New("servermaintenance: the command channel cannot re-attach to a sent command")

var reasonCodePattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// SafeReasonCode keeps an agent-reported code only when it is a bounded
// snake_case token; anything else becomes fallback. A code from the host must
// never make the terminal job write fail.
func SafeReasonCode(code, fallback string) string {
	code = strings.TrimSpace(code)
	if reasonCodePattern.MatchString(code) {
		return code
	}
	return fallback
}

// Refusal is a StackKits denial (exit 3): nothing on the host changed.
type Refusal struct {
	Code    string
	Message string
}

func (r *Refusal) Error() string { return "host maintenance refused: " + r.Code }

// Failure is a StackKits host command that ran and failed (exit 1), with the
// stable failure code of stackkit.host-maintenance/v1.
type Failure struct {
	Code    string
	Message string
}

func (f *Failure) Error() string { return "host maintenance failed: " + f.Code }

// RefusalPackageManagerBusy is the StackKits refusal while apt or dpkg is in
// use. While an apply keeps running after the CLI stopped waiting, a re-plan
// is refused with it, which is how the runner polls the update unit.
const RefusalPackageManagerBusy = "package_manager_busy"

// RefusalStackKitsUpgradeRequired denies a new host-update command before
// dispatch when its configured release cannot safely observe running units.
const RefusalStackKitsUpgradeRequired = "stackkits_upgrade_required"

// FailureHostProbe is the StackKits failure when a host probe cannot finish;
// while an apply's unit runs it is not an install failure.
const FailureHostProbe = "host_probe_failed"

// PlanResult is the settled `stackkit host updates plan` result.
type PlanResult struct {
	PlanDigest    string
	PendingCount  int
	SecurityCount int
	RebootLikely  bool
	Held          []string
	// HoldScope is the StackKits statement of what the hold covers.
	HoldScope string
	// Packages are the packages apply installs, sorted by name.
	Packages []PlanPackage
	// DpkgProblems are `dpkg --audit` lines; apply refuses while any exist.
	DpkgProblems []string
}

// PlanPackage is one package an apply installs.
type PlanPackage struct {
	Name     string
	From     string
	To       string
	Security bool
}

// Bounds of a stored plan result. A plan can list hundreds of packages; the
// job row keeps a bounded, secret-free projection and says when it cut.
const (
	MaxStoredPlanPackages     = 200
	MaxStoredPlanHeld         = 50
	MaxStoredDpkgProblems     = 20
	maxStoredPlanStringLength = 256
)

// PlanResultDocument is the bounded job-result projection of a plan. The
// counts stay exact; the lists are cut at their bounds, and
// packages_truncated says whether packages was cut.
func PlanResultDocument(plan PlanResult) map[string]any {
	packages := make([]any, 0, min(len(plan.Packages), MaxStoredPlanPackages))
	for _, pkg := range plan.Packages {
		if len(packages) == MaxStoredPlanPackages {
			break
		}
		packages = append(packages, map[string]any{
			"name": boundedPlanString(pkg.Name), "from": boundedPlanString(pkg.From),
			"to": boundedPlanString(pkg.To), "security": pkg.Security,
		})
	}
	return map[string]any{
		"plan_digest": plan.PlanDigest, "pending_count": plan.PendingCount, "security_count": plan.SecurityCount,
		"reboot_likely": plan.RebootLikely, "hold_scope": boundedPlanString(plan.HoldScope),
		"held":               boundedPlanStrings(plan.Held, MaxStoredPlanHeld),
		"dpkg_problems":      boundedPlanStrings(plan.DpkgProblems, MaxStoredDpkgProblems),
		"packages":           packages,
		"packages_truncated": len(plan.Packages) > MaxStoredPlanPackages,
	}
}

func boundedPlanStrings(values []string, limit int) []any {
	out := make([]any, 0, min(len(values), limit))
	for _, value := range values {
		if len(out) == limit {
			break
		}
		out = append(out, boundedPlanString(value))
	}
	return out
}

func boundedPlanString(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > maxStoredPlanStringLength {
		value = value[:maxStoredPlanStringLength]
	}
	return value
}

// ApplyResult is the `stackkit host updates apply` result. Outcome is
// applied, noop, failed or running (exit 4: the update unit keeps running).
type ApplyResult struct {
	Outcome        string
	RebootRequired bool
	FailureCode    string
	// Unit is the transient systemd unit running the install.
	Unit    string
	Changed int
}

// RebootResult is the settled `stackkit host reboot` acknowledgement.
type RebootResult struct {
	BootIDBefore string
	ScheduledAt  time.Time
}

// Dispatcher runs one StackKits host command on the node's agent and returns
// its parsed result. A *Refusal error is a denial; any other error is a
// dispatch failure.
type Dispatcher interface {
	Plan(ctx context.Context, target Target) (PlanResult, error)
	Apply(ctx context.Context, target Target, planDigest string) (ApplyResult, error)
	Reboot(ctx context.Context, target Target, delay time.Duration) (RebootResult, error)
}

// Withdrawal reports what withdrawing a job's recorded command found.
type Withdrawal struct {
	// Withdrawn: the command was still queued and now never reaches an agent.
	Withdrawn bool
	// InFlight: an agent received the command and no outcome is recorded yet.
	InFlight bool
}

// CommandWithdrawer is implemented by dispatchers with a durable command
// queue. Expiry withdraws the recorded command first, so a command queued
// before a crash cannot run after its job released the node.
type CommandWithdrawer interface {
	Withdraw(ctx context.Context, target Target) (Withdrawal, error)
}

// ErrDispatchUnavailable is returned while no agent protocol is wired.
var ErrDispatchUnavailable = errors.New("servermaintenance: host maintenance dispatch is not available")

// RebootObservation is what the control plane knows about a node that was
// asked to reboot.
type RebootObservation struct {
	BootIDBefore string
	ScheduledAt  time.Time
	// BootID is the node's currently reported boot id ("" when unknown).
	BootID    string
	Connected bool
	// OfflineSince is when the node's connection last left connected, zero
	// while connected.
	OfflineSince time.Time
	Now          time.Time
}

// RebootVerdict is the watcher's decision; Done false means keep waiting.
type RebootVerdict struct {
	Done       bool
	Succeeded  bool
	ReasonCode string
}

// DecideReboot is the reboot watcher. A connected node reporting a boot id
// other than the one before the request has rebooted. A node that stays
// connected with its old boot id past the guard horizon was not rebooted
// (the StackKits guard abandoned it). A node that went offline has
// ReturnWindow to come back.
func DecideReboot(observation RebootObservation) RebootVerdict {
	bootID := strings.TrimSpace(observation.BootID)
	before := strings.TrimSpace(observation.BootIDBefore)
	if observation.Connected && bootID != "" && before != "" && bootID != before {
		return RebootVerdict{Done: true, Succeeded: true, ReasonCode: ReasonNodeReturned}
	}
	if observation.Connected {
		if observation.Now.After(observation.ScheduledAt.Add(GuardHorizon + ReturnWindow)) {
			return RebootVerdict{Done: true, ReasonCode: ReasonRebootAbandoned}
		}
		return RebootVerdict{}
	}
	wentDown := observation.OfflineSince
	if wentDown.Before(observation.ScheduledAt) {
		wentDown = observation.ScheduledAt
	}
	if observation.Now.After(wentDown.Add(ReturnWindow)) {
		return RebootVerdict{Done: true, ReasonCode: ReasonNodeDidNotReturn}
	}
	return RebootVerdict{}
}
