package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
)

// Service composition errors. The route maps each to one status; nothing else
// decides an HTTP code.
var (
	// ErrJourneyNotFound is an unknown journey id.
	ErrJourneyNotFound = errors.New("onboarding: journey not found")
	// ErrStepNotFound is an action naming a step this journey does not have.
	ErrStepNotFound = errors.New("onboarding: step not found")
	// ErrDerivedOnly is a client reporting completion of a cost-bearing step.
	// It is refused before any write so the client can say why.
	ErrDerivedOnly = errors.New("onboarding: cost-bearing steps complete from server facts only")
	// ErrRevisionConflict is a failed compare-and-swap: another tab moved the
	// record on. The client reloads and retries.
	ErrRevisionConflict = errors.New("onboarding: revision conflict")
	// ErrControlPlaneUnavailable means there is no store to read or write.
	ErrControlPlaneUnavailable = errors.New("onboarding: control plane unavailable")
	// ErrUnknownAction is an action outside the closed vocabulary.
	ErrUnknownAction = errors.New("onboarding: unknown action")
)

// Principal identifies whose journey is being read or written.
type Principal struct {
	TenantID       string
	OwnerSubjectID string
}

// Service composes descriptor, persisted state, derived signals and
// availability into the one payload the client resolves. It deliberately does
// NOT compute the render model: @kombiverselabs/onboarding-core does that, so
// there is exactly one implementation of what the user sees.
type Service struct {
	Store          controlplane.OnboardingStateStore
	Signals        SignalSources
	DeploymentMode config.DeploymentMode
	Features       FeatureChecker
	// Now is injectable for tests; nil means the wall clock.
	Now func() time.Time
}

// Result is the wire payload of both GET and PUT.
type Result struct {
	Journey      json.RawMessage             `json:"journey"`
	State        Record                      `json:"state"`
	Availability map[string]StepAvailability `json:"availability"`
	Revision     int                         `json:"revision"`
}

func (s Service) now() string {
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	return now.UTC().Format(time.RFC3339)
}

// Get resolves the journey for one principal. It is a read: a principal who
// has never acted gets no row, and derived signals that fire on a pristine
// record are returned without being persisted. Writing on GET would turn every
// page load into a write and make "has this user started?" unanswerable.
func (s Service) Get(ctx context.Context, journeyID string, principal Principal) (*Result, error) {
	journey, ok := getJourney(journeyID)
	if !ok {
		return nil, ErrJourneyNotFound
	}
	if s.Store == nil {
		return nil, ErrControlPlaneUnavailable
	}
	now := s.now()

	stored, err := s.Store.GetOnboardingState(ctx, principal.TenantID, principal.OwnerSubjectID, journey.Product, journey.JourneyID)
	if err != nil && !errors.Is(err, controlplane.ErrNotFound) {
		return nil, err
	}
	pristine := stored == nil
	record := migrateRecord(rawState(stored), journey, now)

	reopened, versionChanged := reopenForVersion(record, journey, now)
	record = reopened

	merged, mergeChanged := s.merge(ctx, record, journey, principal, now)
	record = merged

	// A pristine principal stays pristine unless the merge actually found
	// something worth remembering; a version reopen alone never founds a row.
	if mergeChanged || (versionChanged && !pristine) {
		if _, err := s.write(ctx, journey, principal, record, nil); err != nil {
			return nil, err
		}
	}
	return s.result(ctx, journey, record, principal), nil
}

// Apply performs one user-driven action. expectRevision, when set, makes the
// write a compare-and-swap so two tabs cannot silently overwrite each other.
func (s Service) Apply(ctx context.Context, journeyID string, principal Principal, action Action, stepID, source string, expectRevision *int) (*Result, error) {
	journey, ok := getJourney(journeyID)
	if !ok {
		return nil, ErrJourneyNotFound
	}
	if s.Store == nil {
		return nil, ErrControlPlaneUnavailable
	}
	if err := validateAction(journey, action, stepID, source); err != nil {
		return nil, err
	}
	now := s.now()

	stored, err := s.Store.GetOnboardingState(ctx, principal.TenantID, principal.OwnerSubjectID, journey.Product, journey.JourneyID)
	if err != nil && !errors.Is(err, controlplane.ErrNotFound) {
		return nil, err
	}
	record := migrateRecord(rawState(stored), journey, now)
	record, versionChanged := reopenForVersion(record, journey, now)
	record, mergeChanged := s.merge(ctx, record, journey, principal, now)

	var changed bool
	if action == ActionCoachSeen {
		record, changed = markCoachSeen(record, stepID, now)
	} else {
		record, changed = applySignal(record, journey, action, stepID, source, now)
	}

	// A repeated action is a no-op, not an error and not a write — so a
	// double-click cannot lose a compare-and-swap race against itself.
	if changed || mergeChanged || versionChanged {
		if !changed {
			expectRevision = nil
		}
		if _, err := s.write(ctx, journey, principal, record, expectRevision); err != nil {
			return nil, err
		}
	}
	return s.result(ctx, journey, record, principal), nil
}

// validateAction refuses everything that must be refused BEFORE a write, so a
// rejected action leaves no trace.
func validateAction(journey Journey, action Action, stepID, source string) error {
	switch action {
	case ActionDismiss, ActionResume, ActionReset:
		return nil
	case ActionComplete, ActionSkip, ActionCoachSeen:
	default:
		return ErrUnknownAction
	}
	stepID = strings.TrimSpace(stepID)
	if stepID == "" {
		return ErrStepNotFound
	}
	if _, ok := journey.Step(stepID); !ok {
		return ErrStepNotFound
	}
	if costBearingRefusesReported(journey, action, stepID, source) {
		return ErrDerivedOnly
	}
	return nil
}

func (s Service) merge(ctx context.Context, record Record, journey Journey, principal Principal, now string) (Record, bool) {
	signals := resolveDerivedSignals(ctx, s.Signals, principal.TenantID, principal.OwnerSubjectID)
	return mergeDerivedSignals(record, signals, journey, now)
}

func (s Service) result(ctx context.Context, journey Journey, record Record, principal Principal) *Result {
	availability := resolveAvailability(ctx, journey, s.DeploymentMode, s.Features, principal.OwnerSubjectID)
	return &Result{
		Journey:      journey.Raw,
		State:        record,
		Availability: availability,
		Revision:     record.Revision,
	}
}

func (s Service) write(ctx context.Context, journey Journey, principal Principal, record Record, expectRevision *int) (*controlplane.OnboardingState, error) {
	document, err := recordDocument(record)
	if err != nil {
		return nil, err
	}
	state := controlplane.OnboardingState{
		ID:             uuid.NewString(),
		TenantID:       principal.TenantID,
		OwnerSubjectID: principal.OwnerSubjectID,
		Product:        journey.Product,
		JourneyID:      journey.JourneyID,
		JourneyVersion: record.JourneyVersion,
		SchemaVersion:  record.SchemaVersion,
		Status:         record.Status,
		Revision:       record.Revision,
		State:          document,
	}
	saved, err := s.Store.UpsertOnboardingState(ctx, state, expectRevision)
	if errors.Is(err, controlplane.ErrConflict) {
		return nil, ErrRevisionConflict
	}
	if err != nil {
		return nil, err
	}
	return saved, nil
}

// recordDocument turns the typed record back into the stored onboarding-state.v1
// JSON. Round-tripping through the wire shape keeps the column projections and
// the document from drifting apart.
func recordDocument(record Record) (map[string]any, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("onboarding: encode state: %w", err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("onboarding: decode state: %w", err)
	}
	return document, nil
}

func rawState(stored *controlplane.OnboardingState) map[string]any {
	if stored == nil {
		return nil
	}
	return stored.State
}
