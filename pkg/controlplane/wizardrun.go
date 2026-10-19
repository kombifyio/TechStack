package controlplane

import (
	"context"
	"time"
)

const WizardIntentProjectionPending = "intent_projection_pending"
const WizardDesiredProjectionBlocked = "desired_projection_blocked"

func wizardRunBoundRequestReasons() []string {
	return []string{"projection_pending", "intent_projection_failed", WizardIntentProjectionPending, WizardDesiredProjectionBlocked}
}

func wizardRunCanReplace(existing, next WizardRun) bool {
	if existing.Status == "completed" && next.Status != "completed" {
		return false
	}
	if existing.RequestSHA256 == next.RequestSHA256 {
		return true
	}
	if existing.Status == "completed" {
		return false
	}
	for _, reason := range wizardRunBoundRequestReasons() {
		if existing.ErrorReason == reason {
			return false
		}
	}
	return true
}

// WizardRun is one ledger entry of the wizard-run facade (ADR-0036 phase 3).
// A row exists only for runs that reached persistence: completed runs replay
// their Result on an Idempotency-Key re-submit, failed rows record partial
// side effects or a desired-intent save and permit a same-request retry.
// Invalid requests and entitlement denials never consume a key. Valid desired
// choices can be recorded as blocked without creating runtime resources.
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
	// DismissedAt is set when the owner hid this run's dashboard notice. A
	// keyed retry that rewrites the row clears it: a new outcome is news.
	DismissedAt *time.Time
}

// WizardRunStore persists the wizard-run ledger. UpsertWizardRun keys on
// (tenant, owner, idempotency key) when a key is present — a failed row is
// overwritten by the retry's outcome — and plain-inserts keyless runs.
type WizardRunStore interface {
	// ReserveWizardRun atomically binds the key before effects. An existing
	// matching row is returned unchanged, including completed/partial outcomes.
	ReserveWizardRun(ctx context.Context, run WizardRun) (*WizardRun, error)
	GetWizardRunByKey(ctx context.Context, tenantID, ownerSubjectID, idempotencyKey string) (*WizardRun, error)
	// GetLatestWizardRunByOwner returns the owner's most recently written run
	// (by updated_at, so a keyed retry surfaces as the latest activity) —
	// the resume/banner source for GET /api/v1/wizard/runs/active.
	GetLatestWizardRunByOwner(ctx context.Context, tenantID, ownerSubjectID string) (*WizardRun, error)
	UpsertWizardRun(ctx context.Context, run WizardRun) (*WizardRun, error)
	// DismissWizardRun hides one of the owner's runs from the dashboard notice.
	// ErrNotFound when the run does not exist for this owner.
	DismissWizardRun(ctx context.Context, tenantID, ownerSubjectID, runID string, at time.Time) error
}

// AtomicWizardProjectionStore commits a Join's Stack CAS and its existing run
// ledger identity together. Neither side can survive alone. A competing run/key
// or CAS loser commits nothing and cannot claim the winner's Node.
type AtomicWizardProjectionStore interface {
	CommitWizardJoinProjection(context.Context, StackConfigCAS, WizardRun) (*Stack, *WizardRun, error)
}

// WizardPairingPreparationStore binds one durable job to the keyed run before
// mint. Token/result preparation is serialized in the existing job transaction;
// the callback uses the transaction-bound worker writer, never a second store.
type WizardPairingPreparationStore interface {
	PrepareWizardPairing(context.Context, WizardRun, UpsertJobRequest, func(WorkerStore) (map[string]any, error)) (*Job, bool, error)
}
