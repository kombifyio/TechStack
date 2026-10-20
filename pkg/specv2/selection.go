package specv2

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

// GoalSelection is what the pinned release is asked to author: the selected
// goals plus the installing choices the release catalog declares for them.
// Existing platforms are context only; their recorded workloads stay in the seed.
type GoalSelection struct {
	Goals []string
	// Alternatives maps a goal to the non-default alternative it installs
	// (StackSpec workloads.<id>.alternative).
	Alternatives map[string]WorkloadChoice
	// AddOns are separate application workloads selected by installing
	// settings through their workloadRef.
	AddOns []WorkloadChoice
	// Capabilities maps a goal to its capability selections (option id or
	// "off"), passed to the release init as --use-case-capability.
	Capabilities map[string]map[string]string
	// ExistingPlatforms supplies installed same-node platform alternatives as
	// authoring context. Their workloads are never replaced or projected back.
	ExistingPlatforms []WorkloadChoice
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
	return len(selection.Alternatives) > 0 || len(selection.AddOns) > 0 || len(selection.Capabilities) > 0
}

// installedPlatformContext transports catalog-declared platform installations
// on the destination node to StackKits. StackKits alone chooses a compatible
// adapter for the new application; no adapter is inferred from a product name.
func installedPlatformContext(spec map[string]any, catalog stackkitrelease.UseCaseCatalog, nodeID string) []WorkloadChoice {
	workloads, _ := spec["workloads"].(map[string]any)
	control, _ := spec["controlPlane"].(map[string]any)
	members, _ := control["members"].([]any)
	var context []WorkloadChoice
	for _, useCase := range catalog.UseCases {
		entry, exists := workloads[useCase.ID].(map[string]any)
		if !exists || entry["enabled"] == false {
			continue
		}
		placement, _ := entry["placement"].(map[string]any)
		nodes, _ := placement["nodeRefs"].([]any)
		if len(nodes) == 0 {
			nodes = members
		}
		if !slices.Contains(nodes, any(nodeID)) {
			continue
		}
		for _, alternative := range useCase.Alternatives {
			if entry["alternative"] == alternative.ID && alternative.ProvidesRuntimeAdapterRef != "" {
				context = append(context, WorkloadChoice{Goal: useCase.ID, Workload: useCase.ID, Alternative: alternative.ID})
			}
		}
	}
	sort.Slice(context, func(i, j int) bool { return context[i].Workload < context[j].Workload })
	return context
}

// GoalSelectionFromIntent shares the release-authoring choices with planning
// consumers. Both use the same installing settings and workload bindings.
func GoalSelectionFromIntent(intent WizardIntent, catalog stackkitrelease.UseCaseCatalog) (GoalSelection, error) {
	return goalSelectionFromIntent(intent, catalog)
}

// capabilityArgs renders the capability selections of shipped goals as
// `--use-case-capability <goal>.<capability>=<value>` in sorted order.
func (selection GoalSelection) capabilityArgs(mappedGoals []string) []string {
	var args []string
	goals := append([]string(nil), mappedGoals...)
	sort.Strings(goals)
	for _, goal := range goals {
		values := selection.Capabilities[goal]
		ids := make([]string, 0, len(values))
		for id := range values {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			args = append(args, "--use-case-capability", goal+"."+id+"="+values[id])
		}
	}
	return args
}

// goalSelectionFromIntent turns the operator's use-case settings into release
// installing choices. The handler already validated every value against the
// same catalog; this only decides which of them the release installs.
func goalSelectionFromIntent(intent WizardIntent, catalog stackkitrelease.UseCaseCatalog) (GoalSelection, error) {
	selection := GoalSelection{Goals: intent.Goals}
	for slug, values := range intent.UseCaseCapabilities {
		for id, value := range values {
			if selection.Capabilities == nil {
				selection.Capabilities = map[string]map[string]string{}
			}
			if selection.Capabilities[slug] == nil {
				selection.Capabilities[slug] = map[string]string{}
			}
			selection.Capabilities[slug][id] = value
		}
	}
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
