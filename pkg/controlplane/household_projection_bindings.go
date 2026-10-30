package controlplane

import (
	"context"
	"errors"
)

var (
	ErrHouseholdProjectionUnavailable = errors.New("controlplane: verified household projection binding unavailable")
	ErrHouseholdProjectionStale       = errors.New("controlplane: household projection superseded")
	ErrHouseholdProjectionConflict    = errors.New("controlplane: household projection binding conflicts")
)

// HouseholdProjectionIntent is Cloud's monotonic authorization generation for
// one owner/member pair. EffectDigest covers action and canonical app access,
// never mutable profile fields or an activation capability.
type HouseholdProjectionIntent struct {
	TenantID, OwnerSubjectID, CloudSubject string
	Revision                               uint64
	Action, EffectDigest                   string
}

// HouseholdProjectionBinding is retained independently of the D10 link. Once
// chosen, its username, stack and exact enrolled runtime cannot move. An empty
// target is a durable no-dispatch intent/tombstone, not permission to forget the
// generation. It contains neither a link status nor an activation credential.
type HouseholdProjectionBinding struct {
	HouseholdProjectionIntent
	Username, StackID, AgentID, StackKitInstanceID string
}

// HouseholdProjectionStore commits intent before target selection, target
// before dispatch, and the D10 link only while the same generation is current.
// Implementations must be durable; the memory link store deliberately does not
// implement this seam and cannot admit managed node effects.
type HouseholdProjectionStore interface {
	AcceptHouseholdProjection(context.Context, HouseholdProjectionIntent) (*HouseholdProjectionBinding, error)
	BindHouseholdProjectionTarget(context.Context, HouseholdProjectionBinding, string, string, string) (*HouseholdProjectionBinding, error)
	CompleteHouseholdProjection(context.Context, HouseholdProjectionBinding, string, string) (*HomelabIdentityRef, error)
}
