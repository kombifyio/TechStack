package onboarding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

// Predicate ids assigned to this repository in
// kombify-Core/standards/onboarding-step-registry.v1.json. The descriptor
// references them by name; nothing else may invent one.
const (
	PredicateIntentDraftExists   = "techstack.intent_draft_exists"
	PredicateKitDeploymentExists = "techstack.kit_deployment_exists"
	PredicateServerRegistered    = "techstack.server_registered"
	PredicateServiceRunning      = "techstack.service_running"
	// PredicateJourneyCompleted tells Cloud's onboarding hub that this
	// principal finished Techstack's own getting-started journey, so the hub
	// never repeats a Techstack step (ONBOARDING-JOURNEY-STANDARD v1.3 §1).
	PredicateJourneyCompleted = "techstack.journey_completed"
)

// platformJourneyID is the journey whose completion Cloud's hub mirrors.
const platformJourneyID = "techstack.platform"

// SignalSources are the read models a predicate is computed from. Every field
// is optional: a nil store means that one predicate reads false, never that
// the whole resolution fails. Merges are monotonic (mergeDerivedSignals),
// so a false can only ever withhold a completion, never take one back.
type SignalSources struct {
	Homelabs controlplane.HomelabStore
	// KitDeployments adapts the legacy StackStore read model. It is not a
	// second Homelab or a standalone Stack product authority.
	KitDeployments controlplane.StackStore
	Servers        controlplane.ServerRuntimeStore
	Services       controlplane.ServiceRuntimeStore
	// Onboarding is read only for PredicateJourneyCompleted.
	Onboarding controlplane.OnboardingStateStore
}

// OwnerFactsResolution is the principal-scoped snapshot exported to Cloud.
// Facts contains only values whose authority was read successfully. An omitted
// predicate is unknown; it must never be serialized as a confirmed false.
type OwnerFactsResolution struct {
	Facts                 map[string]bool
	Revision              string
	UnavailablePredicates []string
}

type homelabFactReader interface {
	GetHomelabByOwner(ctx context.Context, tenantID, ownerSubjectID string) (*controlplane.Homelab, error)
}

type kitDeploymentFactReader interface {
	ListStacksByTenant(ctx context.Context, tenantID string) ([]controlplane.Stack, error)
}

var errOwnerFactSourceUnavailable = errors.New("onboarding: owner fact source unavailable")

// ResolveOwnerFacts reads the same central sources as resolveDerivedSignals,
// but preserves the difference between confirmed false and unknown. The owner
// API initially exports the two facts consumed by Cloud's platform journey;
// the remaining Techstack predicates stay local until they have the same
// principal-isolation contract.
func ResolveOwnerFacts(ctx context.Context, sources SignalSources, tenantID, ownerSubjectID string) OwnerFactsResolution {
	facts := make(map[string]bool, 2)
	unavailable := make([]string, 0, 2)

	if value, err := readIntentDraftExists(ctx, sources.Homelabs, tenantID, ownerSubjectID); err == nil {
		facts[PredicateIntentDraftExists] = value
	} else {
		unavailable = append(unavailable, PredicateIntentDraftExists)
	}
	if value, err := readKitDeploymentExists(ctx, sources.KitDeployments, tenantID, ownerSubjectID); err == nil {
		facts[PredicateKitDeploymentExists] = value
	} else {
		unavailable = append(unavailable, PredicateKitDeploymentExists)
	}
	if value, err := readJourneyCompleted(ctx, sources.Onboarding, tenantID, ownerSubjectID); err == nil {
		facts[PredicateJourneyCompleted] = value
	} else {
		unavailable = append(unavailable, PredicateJourneyCompleted)
	}

	return OwnerFactsResolution{
		Facts:                 facts,
		Revision:              ownerFactsRevision(facts),
		UnavailablePredicates: unavailable,
	}
}

// readJourneyCompleted is true once the principal finished the Techstack
// platform journey, or once every required step of it is complete. No record
// is a confirmed false; a missing store or a failed read is unknown.
func readJourneyCompleted(ctx context.Context, store controlplane.OnboardingStateStore, tenantID, ownerSubjectID string) (bool, error) {
	if store == nil {
		return false, errOwnerFactSourceUnavailable
	}
	journey, ok := getJourney(platformJourneyID)
	if !ok {
		return false, errOwnerFactSourceUnavailable
	}
	stored, err := store.GetOnboardingState(ctx, tenantID, ownerSubjectID, journey.Product, journey.JourneyID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, errOwnerFactSourceUnavailable
	}
	if stored == nil {
		return false, nil
	}
	record := migrateRecord(rawState(stored), journey, "")
	if record.Status == recordStatusCompleted {
		return true, nil
	}
	done := make(map[string]bool, len(record.Completed))
	for _, entry := range record.Completed {
		done[entry.StepID] = true
	}
	required := 0
	for _, step := range journey.Steps {
		if step.Optional {
			continue
		}
		required++
		if !done[step.ID] {
			return false, nil
		}
	}
	return required > 0, nil
}

func ownerFactsRevision(facts map[string]bool) string {
	keys := make([]string, 0, len(facts))
	for key := range facts {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	hash := sha256.New()
	for _, key := range keys {
		_, _ = hash.Write([]byte(key))
		_, _ = hash.Write([]byte("="))
		_, _ = hash.Write([]byte(strconv.FormatBool(facts[key])))
		_, _ = hash.Write([]byte("\n"))
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

// resolveDerivedSignals computes every derived predicate for one principal.
// A store error is treated exactly like an absent store — that predicate is
// false for this read and the next read may complete it.
func resolveDerivedSignals(ctx context.Context, sources SignalSources, tenantID, ownerSubjectID string) map[string]bool {
	intentDraft, _ := readIntentDraftExists(ctx, sources.Homelabs, tenantID, ownerSubjectID)
	kitDeployment, _ := readKitDeploymentExists(ctx, sources.KitDeployments, tenantID, ownerSubjectID)
	return map[string]bool{
		PredicateIntentDraftExists:   intentDraft,
		PredicateKitDeploymentExists: kitDeployment,
		PredicateServerRegistered:    serverRegistered(ctx, sources.Servers, tenantID, ownerSubjectID),
		PredicateServiceRunning:      serviceRunning(ctx, sources.Services, tenantID),
	}
}

// intentDraftExists is true once the wizard has written the owner's intent
// onto their homelab (internal/routes/stacks/wizard_runs_flow.go writes the
// `wizard` key). It is the earliest server-visible trace that the user
// configured something rather than only looked at the wizard.
func readIntentDraftExists(ctx context.Context, store homelabFactReader, tenantID, ownerSubjectID string) (bool, error) {
	if store == nil {
		return false, errOwnerFactSourceUnavailable
	}
	homelab, err := store.GetHomelabByOwner(ctx, tenantID, ownerSubjectID)
	if errors.Is(err, controlplane.ErrNotFound) || homelab == nil && err == nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	wizard, _ := homelab.Intent["wizard"].(map[string]any)
	return len(wizard) > 0, nil
}

// readKitDeploymentExists projects the legacy stacks table as StackKit
// Deployments. The list is already free of soft-deleted rows, so ownership is
// the only filter.
func readKitDeploymentExists(ctx context.Context, store kitDeploymentFactReader, tenantID, ownerSubjectID string) (bool, error) {
	if store == nil {
		return false, errOwnerFactSourceUnavailable
	}
	deployments, err := store.ListStacksByTenant(ctx, tenantID)
	if err != nil {
		return false, err
	}
	for _, deployment := range deployments {
		if deployment.OwnerSubjectID == ownerSubjectID {
			return true, nil
		}
	}
	return false, nil
}

// serverRegistered counts a runtime the owner actually has, not one that is
// merely planned or already gone.
func serverRegistered(ctx context.Context, store controlplane.ServerRuntimeStore, tenantID, ownerSubjectID string) bool {
	if store == nil {
		return false
	}
	servers, err := store.ListServerRuntimesByTenant(ctx, tenantID, "")
	if err != nil {
		return false
	}
	for _, server := range servers {
		if server.OwnerSubjectID != ownerSubjectID {
			continue
		}
		switch serverregistry.LifecycleState(server.LifecycleState) {
		case serverregistry.LifecyclePlanned, serverregistry.LifecycleDecommissioned:
			continue
		}
		return true
	}
	return false
}

// serviceRunning is tenant-wide, not owner-scoped: service runtimes carry no
// owner. In B2C a tenant is one person, so the projection is exact there; in a
// shared tenant a colleague's running service can complete this step. That is
// deliberate — the step is not cost-bearing and completing it grants nothing
// (ONBOARDING-JOURNEY-STANDARD §3).
func serviceRunning(ctx context.Context, store controlplane.ServiceRuntimeStore, tenantID string) bool {
	if store == nil {
		return false
	}
	services, err := store.ListServiceRuntimes(ctx, tenantID, "", "")
	if err != nil {
		return false
	}
	for _, service := range services {
		if serviceregistry.ObservedState(service.ObservedState) == serviceregistry.ObservedRunning {
			return true
		}
	}
	return false
}
