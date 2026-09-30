package controlplane

import (
	"context"
	"errors"
	"time"
)

var (
	ErrIdentityRevisionConflict = errors.New("controlplane: stack identity revision conflict")
	ErrIdentityMutationConflict = errors.New("controlplane: stack identity mutation id conflict")
)

// ConditionalStackIdentityStore is the durable, owner-scoped write boundary
// for one allowlisted offline mutation. Other HomelabStore implementations do
// not advertise this contract.
type ConditionalStackIdentityStore interface {
	ReadStackIdentitySnapshot(ctx context.Context, tenantID, ownerSubjectID string) (*Homelab, int64, error)
	ApplyStackIdentityMutation(ctx context.Context, input StackIdentityMutation) (*StackIdentityMutationResult, error)
}

type StackIdentityMutation struct {
	TenantID, OwnerSubjectID, HomelabID string
	MutationID, PayloadSHA256           string
	ExpectedRevision                    int64
	Write                               HomelabStackIdentityWrite
}

type StackIdentityMutationResult struct {
	Homelab  *Homelab
	Revision int64
	Replayed bool
}

// Homelab is the umbrella over an owner's kit deployments (ADR-0036). Every
// kit deployment (stacks row) an owner operates belongs to exactly one
// homelab; B2C keeps exactly one active homelab per (tenant, owner).
type Homelab struct {
	ID             string
	TenantID       string
	OwnerSubjectID string
	Name           string
	Intent         map[string]any
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      *time.Time
	// NamedAt records when the owner chose the name. Nil means the row still
	// carries the generated one, which readers must not treat as a chosen
	// name - the dashboard title ranks a chosen name above the kombify Cloud
	// Stack Identity, a generated one below it.
	NamedAt *time.Time
	// Identity is the rest of the owner's Stack Identity; Name above is its
	// name. One homelab carries exactly one Stack Identity (migration 126).
	Identity HomelabStackIdentity
}

// StackIdentityPresentation is how the Stack Identity looks. The ids are
// Brand catalog ids; they never grant anything.
type StackIdentityPresentation struct {
	CharacterID       string  `json:"character_id,omitempty"`
	AnimationStyle    string  `json:"animation_style,omitempty"`
	AnimationEnabled  *bool   `json:"animation_enabled,omitempty"`
	IconStyle         string  `json:"icon_style,omitempty"`
	GlowColorOverride *string `json:"glow_color_override,omitempty"`
}

// HomelabStackIdentity is the sync state of the local Stack Identity copy.
type HomelabStackIdentity struct {
	// Presentation is nil until the identity was set with a character.
	Presentation *StackIdentityPresentation
	// CloudRevision is the kombify Cloud revision this copy last matched; 0
	// means the homelab was never linked to a Cloud identity.
	CloudRevision int
	// Pending marks a local edit kombify Cloud has not confirmed.
	Pending bool
}

// HomelabStackIdentityWrite replaces the Stack Identity of one homelab. EditedAt
// becomes named_at: the time of the edit, local or Cloud's updated_at.
type HomelabStackIdentityWrite struct {
	Name          string
	Presentation  *StackIdentityPresentation
	CloudRevision int
	Pending       bool
	EditedAt      time.Time
	// When set, a Cloud sync step cannot overwrite an identity changed after
	// its read/reconcile snapshot. Legacy online edits leave this nil.
	ExpectedRevision *int64
}

// CreateHomelabRequest carries the caller-owned homelab fields. The ID is
// caller-generated, mirroring CreateStackRequest.
type CreateHomelabRequest struct {
	ID             string
	TenantID       string
	OwnerSubjectID string
	Name           string
	Intent         map[string]any
}

// HomelabStore persists the homelab umbrella. GetOrCreateHomelabForOwner is
// the wizard-run entry point and stays idempotent per (tenant, owner): a
// concurrent create loses against the active-owner singleton and receives the
// already-existing homelab instead of an error.
type HomelabStore interface {
	CreateHomelab(ctx context.Context, req CreateHomelabRequest) (*Homelab, error)
	GetHomelabByOwner(ctx context.Context, tenantID, ownerSubjectID string) (*Homelab, error)
	GetOrCreateHomelabForOwner(ctx context.Context, req CreateHomelabRequest) (*Homelab, error)
	UpdateHomelabIntent(ctx context.Context, tenantID, homelabID string, intent map[string]any) (*Homelab, error)
	// UpdateHomelabName renames the umbrella. The generated name every
	// homelab starts with ("homelab") is not a product name; the owner must
	// be able to correct it.
	// It is a local Stack Identity edit: the row is marked pending until
	// kombify Cloud confirms it.
	UpdateHomelabName(ctx context.Context, tenantID, homelabID, name string) (*Homelab, error)
	// UpdateHomelabStackIdentity writes the whole Stack Identity (name,
	// presentation and sync state) in one statement.
	UpdateHomelabStackIdentity(ctx context.Context, tenantID, homelabID string, write HomelabStackIdentityWrite) (*Homelab, error)
}
