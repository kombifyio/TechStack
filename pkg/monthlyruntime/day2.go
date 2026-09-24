package monthlyruntime

import (
	"context"
	"errors"
	"time"
)

// Managed Day-2 semantics (TS-2, decision 2026-09-22).
//
// Stop and start are an in-place power transition of the SAME resource
// generation. The provider graph, the bound public IPv4, the disk, the lease,
// the capacity reservation and monthly billing persist; decommission remains
// the only destroy. The transition is a generation-bound provider-control
// reconcile with an at-most-once side-effecting step and read-only
// convergence polling, so every provider call is ledgered with receipts.
//
// Owner SSH access is a generation-bound grant for direct SSH to the node's
// execution-channel login. Disable removes every owner-authorized key while
// keeping Kombify's execution-channel key; enable reinstalls the keys the
// owner authorized for this generation. Key material only ever enters through
// the re-authenticated authorize-key route.
//
// Reconnect is a real recovery: when the canonical aggregate does not prove a
// Guard connection, the Guard service is restarted over the execution channel
// and the connection is re-observed within a bounded window.

// PowerState is the desired power target of a start or stop.
type PowerState string

const (
	PowerStateRunning PowerState = "running"
	PowerStateStopped PowerState = "stopped"
)

// PowerRequest asks provider control to converge one exact managed lease
// generation to a power state.
type PowerRequest struct {
	TenantID string
	OwnerID  string
	LeaseID  string
	State    PowerState
}

// PowerOperation is the redacted ledger view of one power reconcile. It names
// the operation and its receipt head, never a provider handle or credential.
type PowerOperation struct {
	OperationID          string     `json:"operation_id"`
	DesiredPowerState    PowerState `json:"desired_power_state"`
	Status               string     `json:"status"`
	Phase                string     `json:"phase"`
	ReasonCode           string     `json:"reason_code,omitempty"`
	Retryable            bool       `json:"retryable,omitempty"`
	Replay               bool       `json:"replay,omitempty"`
	ResourceGenerationID string     `json:"resource_generation_id"`
	ReceiptSequence      uint64     `json:"receipt_sequence"`
	ReceiptDigest        string     `json:"receipt_digest"`
	RequestedAt          time.Time  `json:"requested_at"`
}

// Converged reports a succeeded reconcile head.
func (o PowerOperation) Converged() bool { return o.Status == "succeeded" }

// Failed reports a terminal failed reconcile head.
func (o PowerOperation) Failed() bool { return o.Status == "failed" }

// PowerController executes managed power transitions. Production injects the
// native provider-control implementation; without one start/stop fail closed.
type PowerController interface {
	RequestPower(context.Context, PowerRequest) (PowerOperation, error)
	LatestPower(ctx context.Context, tenantID, leaseID string) (*PowerOperation, error)
}

// SSHAccessRequest changes the generation-bound owner SSH grant.
type SSHAccessRequest struct {
	TenantID             string
	OwnerID              string
	LeaseID              string
	ResourceGenerationID string
	Actor                string
	Enabled              bool
}

// SSHAccessResult is the node-verified outcome of an SSH grant change.
type SSHAccessResult struct {
	Enabled              bool      `json:"enabled"`
	ResourceGenerationID string    `json:"resource_generation_id"`
	OwnerKeysInstalled   int       `json:"owner_keys_installed"`
	OwnerKeysRemoved     int       `json:"owner_keys_removed"`
	OwnerKeysPresent     int       `json:"owner_keys_present"`
	ChangedAt            time.Time `json:"changed_at"`
}

// SSHAccessController applies the owner SSH grant over the execution channel.
type SSHAccessController interface {
	SetOwnerSSHAccess(context.Context, SSHAccessRequest) (SSHAccessResult, error)
}

// AgentReconnectRequest names the exact managed runtime whose Guard should be
// restarted over the execution channel.
type AgentReconnectRequest struct {
	TenantID string
	OwnerID  string
	LeaseID  string
}

// AgentReconnector restarts the node's Guard service over the execution
// channel. It never touches provider resources.
type AgentReconnector interface {
	RestartAgent(context.Context, AgentReconnectRequest) error
}

var (
	// ErrPowerTransitionInProgress rejects a power request while a different
	// power transition of the same generation is still converging.
	ErrPowerTransitionInProgress = errors.New("monthlyruntime: another power transition is still converging")
	// ErrPowerNotAdmitted rejects power when the lease, server or catalog
	// profile does not admit an in-place pause.
	ErrPowerNotAdmitted = errors.New("monthlyruntime: managed power transition is not admitted for this runtime")
	// ErrNodeChannelUnavailable is returned when the execution channel cannot
	// reach the node, so no node change was made.
	ErrNodeChannelUnavailable = errors.New("monthlyruntime: managed node execution channel is unavailable")
	// ErrRuntimeStopped rejects node-channel actions on an intentionally
	// stopped runtime; the owner starts it first.
	ErrRuntimeStopped = errors.New("monthlyruntime: managed runtime is stopped")
)
