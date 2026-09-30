package specv2

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

// GoalSelection is what the pinned release is asked to author: the selected
// goals plus the installing choices the release catalog declares for them.
// Recorded choices never appear here; they stay in the homelab intent.
type GoalSelection struct {
	Goals []string
	// Alternatives maps a goal to the non-default alternative it installs
	// (StackSpec workloads.<id>.alternative).
	Alternatives map[string]WorkloadChoice
	// AddOns are separate application workloads selected by installing
	// settings through their workloadRef.
	AddOns []WorkloadChoice
}

// WorkloadChoice is one release-declared workload binding. Goal is the use
// case the choice belongs to; Workload is set for add-ons.
type WorkloadChoice struct {
	Goal        string
	Workload    string
	Alternative string
	Module      string
}

func (selection GoalSelection) hasInstallChoices() bool {
	return len(selection.Alternatives) > 0 || len(selection.AddOns) > 0
}

// goalSelectionFromIntent turns the operator's use-case settings into release
// installing choices. The handler already validated every value against the
// same catalog; this only decides which of them the release installs.
func goalSelectionFromIntent(intent WizardIntent, catalog stackkitrelease.UseCaseCatalog) (GoalSelection, error) {
	selection := GoalSelection{Goals: intent.Goals}
	slugs := make([]string, 0, len(intent.UseCaseSettings))
	for slug := range intent.UseCaseSettings {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	seen := map[string]bool{}
	for _, slug := range slugs {
		values := intent.UseCaseSettings[slug]
		useCase, ok := catalog.FindUseCase(slug)
		if !ok {
			continue
		}
		if backend, ok := values["backend"].(string); ok {
			if alternative, installs := useCase.InstallingAlternative(strings.TrimSpace(backend)); installs {
				if selection.Alternatives == nil {
					selection.Alternatives = map[string]WorkloadChoice{}
				}
				selection.Alternatives[slug] = WorkloadChoice{Goal: slug, Alternative: alternative.ID, Module: alternative.Modules[0].ID}
			}
		}
		for _, setting := range useCase.Settings {
			if setting.Kind != "toggle" || setting.Realization != "install" || strings.TrimSpace(setting.WorkloadRef) == "" {
				continue
			}
			value, set := values[setting.ID]
			if !set {
				value = setting.Default
			}
			if enabled, _ := value.(bool); !enabled || seen[setting.WorkloadRef] {
				continue
			}
			binding, ok := catalog.DefaultWorkloadBinding(setting.WorkloadRef)
			if !ok {
				return GoalSelection{}, fmt.Errorf("specv2: release catalog declares no installable workload %q for %s setting %q", setting.WorkloadRef, slug, setting.ID)
			}
			seen[setting.WorkloadRef] = true
			selection.AddOns = append(selection.AddOns, WorkloadChoice{
				Goal: slug, Workload: setting.WorkloadRef, Alternative: binding.ID, Module: binding.Modules[0].ID,
			})
		}
	}
	return selection, nil
}

// requireNoAlternativeReplacement keeps a creation run from silently swapping
// the alternative of a workload the deployment already operates. Released
// use cases name their primary workload after themselves.
func requireNoAlternativeReplacement(seed map[string]any, selection GoalSelection) error {
	owned, _ := seed["workloads"].(map[string]any)
	for goal, choice := range selection.Alternatives {
		entry, exists := owned[goal].(map[string]any)
		if exists && entry["alternative"] != choice.Alternative {
			return fmt.Errorf("specv2: existing %s workload requires an explicit migration to %s, not replacement during creation", goal, choice.Alternative)
		}
	}
	return nil
}
