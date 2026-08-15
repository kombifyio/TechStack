// Package runtimelease defines the self-hosted lease boundary used by the
// Open-Core runtime. It deliberately carries only lease identity, ownership,
// validity, and the bound runtime generation.
package runtimelease

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// MaximumWireRevision is the largest exact JSON integer accepted for a lease
// revision. It is safe for PostgreSQL BIGINT and JavaScript consumers.
const MaximumWireRevision uint64 = 1<<53 - 1

// LeaseID is an opaque, stable runtime-lease identifier.
type LeaseID string

// RuntimeServerID is the opaque identity of a durable runtime server.
type RuntimeServerID string

// ResourceGenerationID is the canonical lowercase UUID for the exact runtime
// resource generation that is bound to a lease.
type ResourceGenerationID string

// DesiredState is the caller-owned requested runtime state.
type DesiredState string

// Supported desired runtime states.
const (
	DesiredStateRunning DesiredState = "running"
	DesiredStateStopped DesiredState = "stopped"
	DesiredStateAbsent  DesiredState = "absent"
)

// Lease validation errors.
var (
	ErrInvalidLease     = errors.New("runtimelease: invalid lease")
	ErrLeaseNotYetValid = errors.New("runtimelease: lease not yet valid")
	ErrLeaseExpired     = errors.New("runtimelease: lease expired")
	ErrLeaseCancelled   = errors.New("runtimelease: lease cancelled")
)

// Lease is the secret-free self-host runtime lease projection. It has no
// provider credentials, billing state, inventory payload, or transport data.
type Lease struct {
	ID                   LeaseID              `json:"id"`
	Revision             uint64               `json:"revision"`
	TenantID             string               `json:"tenant_id"`
	OwnerID              string               `json:"owner_id"`
	ServerID             RuntimeServerID      `json:"server_id"`
	ResourceGenerationID ResourceGenerationID `json:"resource_generation_id"`
	DesiredState         DesiredState         `json:"desired_state"`
	ValidFrom            time.Time            `json:"valid_from"`
	ValidUntil           time.Time            `json:"valid_until"`
	RenewedAt            *time.Time           `json:"renewed_at,omitempty"`
	CancelledAt          *time.Time           `json:"cancelled_at,omitempty"`
}

// Validate checks the lease shape and whether it is active at now.
func (l Lease) Validate(now time.Time) error {
	if !validOpaqueID(string(l.ID)) || !validWireRevision(l.Revision) ||
		!validOpaqueID(l.TenantID) || !validOpaqueID(l.OwnerID) ||
		!validOpaqueID(string(l.ServerID)) ||
		!validResourceGenerationID(l.ResourceGenerationID) ||
		!validDesiredState(l.DesiredState) || l.ValidFrom.IsZero() ||
		l.ValidUntil.IsZero() || !l.ValidUntil.After(l.ValidFrom) ||
		(l.RenewedAt != nil && (l.RenewedAt.IsZero() ||
			l.RenewedAt.Before(l.ValidFrom) || l.RenewedAt.After(l.ValidUntil))) ||
		(l.CancelledAt != nil && (l.CancelledAt.IsZero() ||
			l.CancelledAt.After(l.ValidUntil))) {
		return ErrInvalidLease
	}
	if now.Before(l.ValidFrom) {
		return ErrLeaseNotYetValid
	}
	if l.CancelledAt != nil && !l.CancelledAt.After(now) {
		return ErrLeaseCancelled
	}
	if !now.Before(l.ValidUntil) {
		return ErrLeaseExpired
	}
	return nil
}

func validDesiredState(value DesiredState) bool {
	return value == DesiredStateRunning || value == DesiredStateStopped || value == DesiredStateAbsent
}

func validResourceGenerationID(value ResourceGenerationID) bool {
	raw := string(value)
	parsed, err := uuid.Parse(raw)
	return err == nil && parsed != uuid.Nil && parsed.String() == raw
}

func validWireRevision(value uint64) bool {
	return value > 0 && value <= MaximumWireRevision
}

func validOpaqueID(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}
