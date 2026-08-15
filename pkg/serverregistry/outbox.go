package serverregistry

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrOutboxClaimLost means that a claim expired, was superseded, or no
	// longer matches the immutable aggregate head carried by the work item.
	ErrOutboxClaimLost = errors.New("serverregistry: outbox claim lost")
	// ErrOutboxEmpty means that no due item is claimable for the tenant.
	ErrOutboxEmpty = errors.New("serverregistry: no claimable outbox item")
)

// OutboxClaim is a capability for one exact outbox item and server generation.
// ClaimToken is returned only to the worker and is never persisted in plaintext.
type OutboxClaim struct {
	OutboxItem
	ClaimGeneration int64
	ClaimOwner      string
	ClaimToken      string `json:"-"`
	ClaimExpiresAt  time.Time
}

// OutboxWorkRepository owns tenant-scoped claim, heartbeat, completion, and
// retry operations. Every mutation checks the claim token, claim generation,
// aggregate revision, and server generation against database time.
type OutboxWorkRepository interface {
	ClaimOutbox(context.Context, string, string, time.Duration) (*OutboxClaim, error)
	HeartbeatOutbox(context.Context, OutboxClaim, time.Duration) error
	CompleteOutbox(context.Context, OutboxClaim) error
	RetryOutbox(context.Context, OutboxClaim, time.Duration, error) error
}
