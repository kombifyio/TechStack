// Package stackrouting owns the tenant-scoped desired routing state for a
// StackKit rollout. Routing is an overlay on the immutable user intent: the
// overlay is applied only while deriving rollout artifacts.
package stackrouting

import (
	"context"
	"errors"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

const (
	ModeCustomDomain = "custom-domain"

	RolloutNotRequested = "not_requested"
	RolloutPending      = "pending"
	RolloutCompleted    = "completed"
	RolloutFailed       = "failed"

	ReasonRolloutNotDispatched = "routing_rollout_not_dispatched"
	ReasonRolloutFailed        = "routing_rollout_failed"
)

var (
	ErrNotFound            = errors.New("stackrouting: not found")
	ErrForbidden           = errors.New("stackrouting: forbidden")
	ErrInvalid             = errors.New("stackrouting: invalid request")
	ErrUnavailable         = errors.New("stackrouting: unavailable")
	ErrRevisionConflict    = errors.New("stackrouting: revision conflict")
	ErrIdempotencyConflict = errors.New("stackrouting: idempotency conflict")
	ErrRolloutInProgress   = errors.New("stackrouting: rollout in progress")
)

// Principal is trusted request identity after the product auth boundary.
type Principal struct {
	TenantID       string
	OwnerSubjectID string
}

// Provenance records why and through which DNS authority a custom domain was
// selected. It intentionally carries references, never provider credentials.
type Provenance struct {
	Source           string `json:"source"`
	DNSProvider      string `json:"dns_provider"`
	ZoneID           string `json:"zone_id,omitempty"`
	ExternalDomainID string `json:"external_domain_id,omitempty"`
}

// DesiredState is the durable, revisioned routing overlay for exactly one
// stack/server/lease tuple.
type DesiredState struct {
	TenantID       string     `json:"-"`
	StackID        string     `json:"stack_id"`
	OwnerSubjectID string     `json:"-"`
	ServerID       string     `json:"server_id"`
	LeaseID        string     `json:"lease_id,omitempty"`
	Revision       int64      `json:"revision"`
	Mode           string     `json:"mode"`
	Domain         string     `json:"domain"`
	Provenance     Provenance `json:"provenance"`
	RolloutStatus  string     `json:"rollout_status"`
	RolloutJobID   string     `json:"rollout_job_id,omitempty"`
	ReasonCode     string     `json:"reason_code,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// EnsureInput is the API/application-service input. Idempotency and optimistic
// concurrency are transport metadata and are therefore kept separate.
type EnsureInput struct {
	ServerID      string
	LeaseID       string
	Mode          string
	Domain        string
	Provenance    Provenance
	EnsureRollout bool
	// RequireLease is set by managed service-to-service handovers. Public
	// user-owned/local routing leaves it false and still rejects an invented
	// lease when the exact server has none.
	RequireLease bool
}

type MutationOptions struct {
	IdempotencyKey   string
	ExpectedRevision *int64
}

// PutRequest is the normalized atomic persistence command.
type PutRequest struct {
	DesiredState
	IdempotencyKey   string
	RequestHash      string
	ExpectedRevision *int64
}

type PutResult struct {
	State  *DesiredState
	Replay bool
}

// View makes desired, observed, and rollout truth explicit. Persisting desired
// state never implies that DNS/TLS/runtime state has already converged.
type View struct {
	Desired          DesiredState  `json:"desired"`
	Observed         ObservedState `json:"observed"`
	Rollout          RolloutState  `json:"rollout"`
	IdempotentReplay bool          `json:"idempotent_replay,omitempty"`
}

type ObservedState struct {
	Status          string `json:"status"`
	Applied         bool   `json:"applied"`
	AppliedRevision int64  `json:"applied_revision"`
}

type RolloutState struct {
	Status     string `json:"status"`
	JobID      string `json:"job_id,omitempty"`
	ReasonCode string `json:"reason_code,omitempty"`
}

type Store interface {
	Get(ctx context.Context, tenantID, stackID string) (*DesiredState, error)
	Put(ctx context.Context, req PutRequest) (*PutResult, error)
	MarkRolloutDispatched(ctx context.Context, tenantID, stackID string, expectedRevision int64, jobID string) (*DesiredState, error)
	MarkRolloutFinished(ctx context.Context, tenantID, stackID string, expectedRevision int64, jobID, terminalStatus, reasonCode string) (*DesiredState, error)
}

// ManagedLeaseLister is the read-only authority seam used to validate one
// explicitly supplied managed RuntimeLease. Implementations may return a
// tenant list; routing still resolves only the requested lease ID and never
// auto-selects a target.
type ManagedLeaseLister interface {
	ListByTenant(ctx context.Context, tenantID string) ([]vmlease.Lease, error)
}

type RolloutRequest struct {
	TenantID        string
	OwnerSubjectID  string
	StackID         string
	ServerID        string
	LeaseID         string
	RoutingRevision int64
	IdempotencyKey  string
}

type RolloutResult struct {
	JobID string
}

// Target is an explicit owner-visible routing destination. Listing targets is
// discovery only: callers must choose and send the complete tuple back.
type Target struct {
	StackID         string `json:"stack_id"`
	ServerID        string `json:"server_id"`
	LeaseID         string `json:"lease_id"`
	Role            string `json:"role"`
	Provider        string `json:"provider"`
	Address         string `json:"address,omitempty"`
	LifecycleState  string `json:"lifecycle_state"`
	ConnectionState string `json:"connection_state"`
}

// RolloutDispatcher must reuse the exact validated server/lease target. A
// dispatcher may enqueue existing deploy machinery, but must fail instead of
// selecting a different lease or creating provider infrastructure.
type RolloutDispatcher interface {
	DispatchRoutingRollout(ctx context.Context, req RolloutRequest) (*RolloutResult, error)
}
