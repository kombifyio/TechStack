package controlplane

import (
	"context"
	"time"
)

// WizardRun is one ledger entry of the wizard-run facade (ADR-0036 phase 3).
// A row exists only for runs that reached persistence: completed runs replay
// their Result on an Idempotency-Key re-submit, failed rows record partial
// side effects and never block a retry. Pre-persist rejections (validation,
// entitlement, projection) never write a row, so a rejected key stays usable.
type WizardRun struct {
	ID             string
	TenantID       string
	OwnerSubjectID string
	// IdempotencyKey is empty for runs submitted without a key; such runs are
	// audit-only and never replayed.
	IdempotencyKey string
	// RequestSHA256 fingerprints the run request; a completed row replays only
	// when the fingerprint matches, otherwise the key re-use is a conflict.
	RequestSHA256 string
	// RunKind is the effective kind after coercion; RequestedRunKind preserves
	// what the client asked for (a second first-run is coerced to expansion).
	RunKind          string
	RequestedRunKind string
	HomelabID        string
	StackID          string
	NodeID           string
	JobID            string
	PairingJobID     string
	Status           string
	Intent           map[string]any
	Result           map[string]any
	ErrorReason      string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// WizardRunStore persists the wizard-run ledger. UpsertWizardRun keys on
// (tenant, owner, idempotency key) when a key is present — a failed row is
// overwritten by the retry's outcome — and plain-inserts keyless runs.
type WizardRunStore interface {
	GetWizardRunByKey(ctx context.Context, tenantID, ownerSubjectID, idempotencyKey string) (*WizardRun, error)
	// GetLatestWizardRunByOwner returns the owner's most recently written run
	// (by updated_at, so a keyed retry surfaces as the latest activity) —
	// the resume/banner source for GET /api/v1/wizard/runs/active.
	GetLatestWizardRunByOwner(ctx context.Context, tenantID, ownerSubjectID string) (*WizardRun, error)
	UpsertWizardRun(ctx context.Context, run WizardRun) (*WizardRun, error)
}
