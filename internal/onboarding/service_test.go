package onboarding

import (
	"context"
	"errors"
	"testing"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
)

func testService(store *controlplane.MemoryStore) Service {
	return Service{
		Store:          store,
		Signals:        signalSources(store),
		DeploymentMode: config.ModeSelfHosted,
		Features:       stubChecker{enabled: true},
	}
}

func testPrincipal() Principal {
	return Principal{TenantID: signalTenant, OwnerSubjectID: signalOwner}
}

func storedRow(t *testing.T, store *controlplane.MemoryStore) *controlplane.OnboardingState {
	t.Helper()
	row, err := store.GetOnboardingState(context.Background(), signalTenant, signalOwner, "techstack", "techstack.platform")
	if err != nil && !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatalf("read row: %v", err)
	}
	return row
}

// Reading is a read. If a page load founded a row, "has this user started?"
// would answer yes for everyone who ever opened the dashboard.
func TestGetDoesNotWriteForAPristinePrincipal(t *testing.T) {
	store := controlplane.NewMemoryStore()
	result, err := testService(store).Get(context.Background(), "techstack.platform", testPrincipal())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(result.State.Completed) != 0 || result.Revision != 0 {
		t.Fatalf("a fresh principal starts empty: %+v", result.State)
	}
	if row := storedRow(t, store); row != nil {
		t.Fatalf("a read founded a row: %+v", row)
	}
}

// A server fact the user produced elsewhere completes its step without the
// client ever asserting anything.
func TestGetPersistsADerivedCompletionItDiscovers(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-mine", TenantID: signalTenant, OwnerSubjectID: signalOwner, Name: "mine", Mode: "cloud", Status: "running",
	}); err != nil {
		t.Fatalf("seed legacy deployment row: %v", err)
	}

	result, err := testService(store).Get(ctx, "techstack.platform", testPrincipal())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !result.State.hasCompleted("techstack.deploy_stackkit") {
		t.Fatalf("the StackKit Deployment predicate should have completed the rollout step: %+v", result.State)
	}
	// A derived merge is not a user action and must not bump the revision the
	// client compare-and-swaps against.
	if result.Revision != 0 {
		t.Fatalf("a derived merge bumped the revision to %d", result.Revision)
	}
	if storedRow(t, store) == nil {
		t.Fatal("a discovered completion must be persisted")
	}
}

func TestSkipPersistsAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	service := testService(store)

	first, err := service.Apply(ctx, "techstack.platform", testPrincipal(), ActionSkip, "techstack.deploy_service", "", nil)
	if err != nil {
		t.Fatalf("skip: %v", err)
	}
	if !first.State.hasCompleted("techstack.deploy_service") || first.Revision != 1 {
		t.Fatalf("skip should complete and bump: %+v", first.State)
	}

	second, err := service.Apply(ctx, "techstack.platform", testPrincipal(), ActionComplete, "techstack.deploy_service", "", nil)
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if second.Revision != 1 {
		t.Fatalf("a repeated completion is a no-op, got revision %d", second.Revision)
	}
}

// The one rule that must hold before any write: a client cannot report itself
// through a step that spends money.
func TestReportedCompletionOfACostBearingStepIsRefusedWithoutAWrite(t *testing.T) {
	store := controlplane.NewMemoryStore()
	_, err := testService(store).Apply(context.Background(), "techstack.platform", testPrincipal(), ActionComplete, "techstack.deploy_stackkit", "reported", nil)
	if !errors.Is(err, ErrDerivedOnly) {
		t.Fatalf("want ErrDerivedOnly, got %v", err)
	}
	if row := storedRow(t, store); row != nil {
		t.Fatalf("a refused action left a row: %+v", row)
	}
}

func TestStaleRevisionConflicts(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	service := testService(store)

	if _, err := service.Apply(ctx, "techstack.platform", testPrincipal(), ActionSkip, "techstack.deploy_service", "", nil); err != nil {
		t.Fatalf("first action: %v", err)
	}
	stale := 0
	_, err := service.Apply(ctx, "techstack.platform", testPrincipal(), ActionDismiss, "", "", &stale)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("want ErrRevisionConflict, got %v", err)
	}
}

func TestUnknownJourneyAndStepAreDistinguished(t *testing.T) {
	store := controlplane.NewMemoryStore()
	service := testService(store)
	if _, err := service.Get(context.Background(), "nope.platform", testPrincipal()); !errors.Is(err, ErrJourneyNotFound) {
		t.Fatalf("want ErrJourneyNotFound, got %v", err)
	}
	if _, err := service.Apply(context.Background(), "techstack.platform", testPrincipal(), ActionComplete, "techstack.nope", "", nil); !errors.Is(err, ErrStepNotFound) {
		t.Fatalf("want ErrStepNotFound, got %v", err)
	}
}

// Availability rides on every response, so the client never has to guess why a
// step it can see is not something this account may do.
func TestResultCarriesTheDescriptorVerbatimAndAvailability(t *testing.T) {
	store := controlplane.NewMemoryStore()
	result, err := testService(store).Get(context.Background(), "techstack.platform", testPrincipal())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	journey := testJourney(t)
	if string(result.Journey) != string(journey.Raw) {
		t.Fatal("the descriptor must be served verbatim")
	}
	if len(result.Availability) == 0 {
		t.Fatal("self-host must report its cost-bearing steps as unavailable")
	}
}
