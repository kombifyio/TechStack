package stacks

import (
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/specv2"
)

type wizardBlockedCapability struct {
	UseCase    string `json:"use_case"`
	Capability string `json:"capability"`
	Selection  string `json:"selection"`
}

// The guided tool answer records the desired catalog option separately from
// execution capabilities. A recorded required option blocks projection; it
// never makes "off" legal in an actual generated or dispatched plan.
func admitWizardDesiredCapabilities(intent specv2.WizardIntent) (specv2.WizardIntent, []wizardBlockedCapability, error) {
	if len(intent.UseCaseCapabilities) == 0 && len(intent.UseCaseSetupAnswers) == 0 {
		return intent, nil, nil
	}
	catalog, err := stackkitrelease.ResolveUseCaseCatalog()
	if err != nil {
		return intent, nil, err
	}
	var blocked []wizardBlockedCapability
	// Link handoffs can carry only guided answers. A missing execution map
	// must not turn a recorded required choice into the release's default.
	for slug, answers := range intent.UseCaseSetupAnswers {
		useCase, ok := catalog.FindUseCase(slug)
		if !ok {
			return intent, nil, fmt.Errorf("use case %q is not in the catalog", slug)
		}
		for _, capability := range useCase.Capabilities {
			if !capability.Required {
				continue
			}
			if _, explicit := intent.UseCaseCapabilities[slug][capability.ID]; explicit {
				continue
			}
			answer := answers["tool-"+capability.ID]
			options := []stackkitrelease.UseCaseCapabilityOption{capability.Default}
			if capability.Alternative != nil {
				options = append(options, *capability.Alternative)
			}
			for _, option := range options {
				if len(answer) == 1 && answer[0] == option.ID && option.Realization == "recorded" {
					blocked = append(blocked, wizardBlockedCapability{slug, capability.ID, option.ID})
				}
			}
		}
	}
	effective := make(map[string]map[string]string, len(intent.UseCaseCapabilities))
	for slug, values := range intent.UseCaseCapabilities {
		useCase, ok := catalog.FindUseCase(slug)
		if !ok {
			return intent, nil, fmt.Errorf("use case %q is not in the catalog", slug)
		}
		effective[slug] = make(map[string]string, len(values))
		for id, value := range values {
			capability, ok := useCase.FindCapability(id)
			if !ok {
				return intent, nil, fmt.Errorf("use case %q declares no capability %q", slug, id)
			}
			effective[slug][id] = value
			if capability.Required {
				recorded := false
				answer := intent.UseCaseSetupAnswers[slug]["tool-"+id]
				options := []stackkitrelease.UseCaseCapabilityOption{capability.Default}
				if capability.Alternative != nil {
					options = append(options, *capability.Alternative)
				}
				for _, option := range options {
					if len(answer) != 1 || answer[0] != option.ID {
						continue
					}
					if option.Realization == "recorded" {
						if value != "off" && value != option.ID {
							return intent, nil, fmt.Errorf("use case %q capability %q conflicts with its desired recorded option", slug, id)
						}
						blocked = append(blocked, wizardBlockedCapability{slug, id, option.ID})
						recorded = true
					}
				}
				if recorded {
					continue
				}
			}
			if err := capability.AcceptsSelection(value); err != nil {
				return intent, nil, fmt.Errorf("use case %q: %w", slug, err)
			}
		}
	}
	sort.Slice(blocked, func(i, j int) bool {
		if blocked[i].UseCase != blocked[j].UseCase {
			return blocked[i].UseCase < blocked[j].UseCase
		}
		return blocked[i].Capability < blocked[j].Capability
	})
	if len(intent.UseCaseCapabilities) > 0 {
		intent.UseCaseCapabilities = effective
	}
	return intent, blocked, nil
}

// Reserve the existing run identity before the canonical intent mutation.
// A failed save remains a keyed, resumable projection failure, without a spec,
// stack, pairing token or job. Concurrent disjoint decisions still use the
// existing atomic Homelab intent merge.
func (h wizardRunHandlers) persistBlockedWizardRun(e *httpx.Event, run *wizardRunState) error {
	outcome := controlplane.WizardRun{}
	if run.priorFailed != nil {
		outcome = *run.priorFailed
	}
	if run.resumeStack != nil || outcome.NodeID != "" || outcome.JobID != "" || outcome.PairingJobID != "" {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"The existing run has runtime state; its recovery must finish before a blocked projection can replace it", nil)
	}
	if run.request.Intent.KitAssignment.Mode == wizardRunKindJoin {
		stack, err := h.crud.findOwnedStoreStack(e, run.request.Intent.KitAssignment.KitDeploymentID)
		if err != nil {
			return err
		}
		outcome.StackID = stack.ID
	}
	result := map[string]any{
		"intent_projection_status": "blocked",
		"blocked_capabilities":     run.blockedCapabilities,
		"unmapped_goals":           append([]string{}, run.request.Intent.Goals...),
	}
	if err := h.writeWizardRunLedger(e.Request.Context(), run, outcome, "failed", controlplane.WizardIntentProjectionPending, result); err != nil {
		if errors.Is(err, controlplane.ErrConflict) {
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Idempotency-Key already identifies a different or completed projection", nil)
		}
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Wizard run reservation unavailable; retry with the same Idempotency-Key", nil)
	}
	if h.ensureWizardHomelab(e, run, run.request.Intent.Name) {
		return nil
	}
	projection := &specv2.Projection{UnmappedGoals: append([]string{}, run.request.Intent.Goals...)}
	run.evaluatedGoals = mergeWizardGoals(run.request.Intent.Goals)
	if err := h.persistWizardHomelabIntent(e.Request.Context(), run, outcome.StackID, projection); err != nil {
		return h.failWizardIntentProjection(e, run, outcome.StackID, projection, err)
	}
	if err := h.writeWizardRunLedger(e.Request.Context(), run, outcome, "failed", controlplane.WizardDesiredProjectionBlocked, result); err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Decisions saved; retry with the same Idempotency-Key to finish the run record", nil)
	}
	details := wizardRunDetails("wizard_desired_projection_blocked", false,
		"Decisions saved; setup is waiting", "This release cannot realize a required choice. No new runtime operation was dispatched. Continue from the saved decisions when a release supports that choice.")
	details["run_id"], details["homelab_id"] = run.runID, run.homelabID
	details["intent_projection_status"] = "blocked"
	details["blocked_capabilities"] = run.blockedCapabilities
	return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Desired decisions saved; runtime projection blocked", details)
}
