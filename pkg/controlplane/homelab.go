package controlplane

import (
	"context"
	"time"
)

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
	UpdateHomelabName(ctx context.Context, tenantID, homelabID, name string) (*Homelab, error)
}
