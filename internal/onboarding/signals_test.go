package onboarding

import (
	"context"
	"errors"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

type failingHomelabFactStore struct{ controlplane.HomelabStore }

func (failingHomelabFactStore) GetHomelabByOwner(context.Context, string, string) (*controlplane.Homelab, error) {
	return nil, errors.New("homelab read failed")
}

type failingKitDeploymentFactStore struct{ controlplane.StackStore }

func (failingKitDeploymentFactStore) ListStacksByTenant(context.Context, string) ([]controlplane.Stack, error) {
	return nil, errors.New("deployment read failed")
}

const (
	signalTenant = "tenant-signals"
	signalOwner  = "auth0|owner-signals"
	signalOther  = "auth0|somebody-else"
)

func signalSources(store *controlplane.MemoryStore) SignalSources {
	return SignalSources{Homelabs: store, KitDeployments: store, Servers: store, Services: store, Onboarding: store}
}

// A principal who has done nothing has no derived completions — the journey a
// new user sees is genuinely empty, not accidentally pre-ticked.
func TestNoSignalsFireForAFreshPrincipal(t *testing.T) {
	store := controlplane.NewMemoryStore()
	got := resolveDerivedSignals(context.Background(), signalSources(store), signalTenant, signalOwner)
	for predicate, fired := range got {
		if fired {
			t.Errorf("%s fired for a principal with no data", predicate)
		}
	}
}

// The predicates must read the owner's own data. A colleague's deployment or server
// in the same tenant may not complete this user's onboarding — Techstack's RLS
// is tenant-only, so same tenant never implies same owner.
func TestOwnerScopedSignalsIgnoreAnotherPrincipalsData(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()

	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-other", TenantID: signalTenant, OwnerSubjectID: signalOther, Name: "theirs", Mode: "cloud", Status: "running",
	}); err != nil {
		t.Fatalf("seed legacy deployment row: %v", err)
	}
	if _, err := store.UpsertServerRuntime(ctx, controlplane.ServerRuntime{
		ID: "srv-other", TenantID: signalTenant, OwnerSubjectID: signalOther, LifecycleState: string(serverregistry.LifecycleActive),
	}); err != nil {
		t.Fatalf("seed server: %v", err)
	}

	got := resolveDerivedSignals(ctx, signalSources(store), signalTenant, signalOwner)
	if got[PredicateKitDeploymentExists] {
		t.Error("another owner's StackKit Deployment completed this principal's rollout step")
	}
	if got[PredicateServerRegistered] {
		t.Error("another owner's server completed this principal's add-server step")
	}
}

func TestSignalsFireOnTheOwnersOwnData(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()

	homelab, err := store.GetOrCreateHomelabForOwner(ctx, controlplane.CreateHomelabRequest{
		ID: "hl-1", TenantID: signalTenant, OwnerSubjectID: signalOwner, Name: "homelab",
	})
	if err != nil {
		t.Fatalf("seed homelab: %v", err)
	}
	if _, err := store.UpdateHomelabIntent(ctx, signalTenant, homelab.ID, map[string]any{
		"wizard": map[string]any{"goals": []string{"host a website"}},
	}); err != nil {
		t.Fatalf("seed intent: %v", err)
	}
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-mine", TenantID: signalTenant, OwnerSubjectID: signalOwner, Name: "mine", Mode: "cloud", Status: "running",
	}); err != nil {
		t.Fatalf("seed legacy deployment row: %v", err)
	}
	if _, err := store.UpsertServerRuntime(ctx, controlplane.ServerRuntime{
		ID: "srv-mine", TenantID: signalTenant, OwnerSubjectID: signalOwner, LifecycleState: string(serverregistry.LifecycleActive),
	}); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	if _, err := store.UpsertServiceRuntime(ctx, controlplane.ServiceRuntime{
		ID: "svc-mine", TenantID: signalTenant, StackID: "stack-mine", ServerID: "srv-mine",
		ServiceKey: "web", Name: "web", ObservedState: string(serviceregistry.ObservedRunning),
	}); err != nil {
		t.Fatalf("seed service: %v", err)
	}

	got := resolveDerivedSignals(ctx, signalSources(store), signalTenant, signalOwner)
	for _, predicate := range []string{PredicateIntentDraftExists, PredicateKitDeploymentExists, PredicateServerRegistered, PredicateServiceRunning} {
		if !got[predicate] {
			t.Errorf("%s should have fired: %+v", predicate, got)
		}
	}
}

// A server that only exists on paper is not a server the user added.
func TestAPlannedServerDoesNotCountAsRegistered(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertServerRuntime(ctx, controlplane.ServerRuntime{
		ID: "srv-planned", TenantID: signalTenant, OwnerSubjectID: signalOwner, LifecycleState: string(serverregistry.LifecyclePlanned),
	}); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	if resolveDerivedSignals(ctx, signalSources(store), signalTenant, signalOwner)[PredicateServerRegistered] {
		t.Error("a planned server must not complete the add-server step")
	}
}

// Absent stores are the self-host and degraded cases; they must read false
// rather than fail the whole resolution.
func TestMissingStoresResolveToFalseNotAnError(t *testing.T) {
	got := resolveDerivedSignals(context.Background(), SignalSources{}, signalTenant, signalOwner)
	if len(got) == 0 {
		t.Fatal("every predicate must be reported even without stores")
	}
	for predicate, fired := range got {
		if fired {
			t.Errorf("%s fired without a store behind it", predicate)
		}
	}
}

func TestOwnerFactsDistinguishConfirmedFalseFromUnknown(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	known := ResolveOwnerFacts(ctx, signalSources(store), signalTenant, signalOwner)
	if len(known.UnavailablePredicates) != 0 {
		t.Fatalf("available stores reported unknown predicates: %v", known.UnavailablePredicates)
	}
	for _, predicate := range []string{PredicateIntentDraftExists, PredicateKitDeploymentExists, PredicateJourneyCompleted} {
		value, present := known.Facts[predicate]
		if !present || value {
			t.Fatalf("%s = %v, present=%v; want confirmed false", predicate, value, present)
		}
	}

	unknown := ResolveOwnerFacts(ctx, SignalSources{
		Homelabs:       failingHomelabFactStore{},
		KitDeployments: failingKitDeploymentFactStore{},
	}, signalTenant, signalOwner)
	if len(unknown.Facts) != 0 {
		t.Fatalf("failed reads became confirmed facts: %v", unknown.Facts)
	}
	// Without an onboarding store the journey fact is unknown too.
	if len(unknown.UnavailablePredicates) != 3 {
		t.Fatalf("unknown predicates = %v", unknown.UnavailablePredicates)
	}
}

func TestOwnerFactsRevisionChangesOnlyWithTheOwnedSnapshot(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	baseline := ResolveOwnerFacts(ctx, signalSources(store), signalTenant, signalOwner)

	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-other-revision", TenantID: signalTenant, OwnerSubjectID: signalOther, Name: "theirs", Mode: "cloud", Status: "running",
	}); err != nil {
		t.Fatalf("seed other deployment: %v", err)
	}
	other := ResolveOwnerFacts(ctx, signalSources(store), signalTenant, signalOwner)
	if other.Revision != baseline.Revision {
		t.Fatal("another principal changed this owner's revision")
	}

	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-owner-revision", TenantID: signalTenant, OwnerSubjectID: signalOwner, Name: "mine", Mode: "cloud", Status: "running",
	}); err != nil {
		t.Fatalf("seed owner deployment: %v", err)
	}
	owned := ResolveOwnerFacts(ctx, signalSources(store), signalTenant, signalOwner)
	if owned.Revision == baseline.Revision || !owned.Facts[PredicateKitDeploymentExists] {
		t.Fatalf("owned snapshot did not advance: before=%s after=%s facts=%v", baseline.Revision, owned.Revision, owned.Facts)
	}
}

// Cloud's hub mirrors a finished Techstack journey; a journey the principal
// has only started stays an open hub row.
func TestOwnerFactsReportAFinishedPlatformJourney(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	journey, ok := getJourney(platformJourneyID)
	if !ok {
		t.Fatalf("journey %s not shipped", platformJourneyID)
	}
	write := func(status string) {
		t.Helper()
		if _, err := store.UpsertOnboardingState(ctx, controlplane.OnboardingState{
			ID:             "onboarding-" + status,
			TenantID:       signalTenant,
			OwnerSubjectID: signalOwner,
			Product:        journey.Product,
			JourneyID:      journey.JourneyID,
			JourneyVersion: journey.JourneyVersion,
			SchemaVersion:  1,
			Status:         status,
			State: map[string]any{
				"schema_version":  1,
				"journey_id":      journey.JourneyID,
				"journey_version": journey.JourneyVersion,
				"completed":       []any{},
				"seen_steps":      []any{},
				"status":          status,
				"revision":        0,
				"updated_at":      "2026-09-11T00:00:00Z",
			},
		}, nil); err != nil {
			t.Fatal(err)
		}
	}

	write("active")
	if got := ResolveOwnerFacts(ctx, signalSources(store), signalTenant, signalOwner).Facts[PredicateJourneyCompleted]; got {
		t.Fatal("a started journey was reported as completed")
	}
	write("completed")
	if got := ResolveOwnerFacts(ctx, signalSources(store), signalTenant, signalOwner).Facts[PredicateJourneyCompleted]; !got {
		t.Fatal("a finished journey was not reported")
	}
	if ResolveOwnerFacts(ctx, signalSources(store), signalTenant, signalOther).Facts[PredicateJourneyCompleted] {
		t.Fatal("another principal inherited the finished journey")
	}
}
