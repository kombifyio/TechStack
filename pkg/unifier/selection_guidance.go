package unifier

import (
	"sort"
	"strings"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/core"
)

// Selection guidance codes shared with the Wizard message catalog.
const (
	SelectionContextNone          = "none"
	SelectionContextInventory     = "inventory"
	SelectionContextExperience    = "experience"
	SelectionWithheldAdvanced     = "advanced_for_experience"
	SelectionWithheldCapacity     = "capacity"
	SelectionWithheldLocalAI      = "local_ai_hardware"
	SelectionTipLocalAI           = "local_ai_needs_stronger_device"
	SelectionTipMoreDevices       = "more_devices"
	selectionReferenceDeviceRAMMB = 8192
	selectionFullHomelabGoals     = 6
)

// selectionPriority orders goals when capacity is short: everyday first.
var selectionPriority = []string{
	"files", "photos", "vault", "mail", "media", "smart-home", "marketplace",
	"ai", "automation", "dev", "game", "network",
}

var selectionAdvanced = map[string]bool{"dev": true, "network": true, "automation": true}

// GuideWizardSelection derives the preselection for the full-homelab intent and
// the single most useful tip for the current selection from the operator's
// disclosed experience and the tenant's worker inventory.
func GuideWizardSelection(request core.WizardRecommendationRequest, input WizardRecommendationEvaluation) *core.WizardSelectionGuidance {
	selected := request.Goals
	wantsPreselect := strings.EqualFold(strings.TrimSpace(request.SelectionIntent), "all")
	if !wantsPreselect && len(selected) < 2 {
		return nil
	}
	workers := onlineWorkers(input.Inventory)
	experience := -1
	if profile := OperatorCapabilityFromSelfDisclosure(input.SelfDisclosure, input.SelfDisclosureUpdatedAt); profile != nil {
		experience = profile.Score
	}
	guidance := &core.WizardSelectionGuidance{Context: []string{}}
	if len(workers) > 0 {
		guidance.Context = append(guidance.Context, SelectionContextInventory)
	}
	if experience >= 0 {
		guidance.Context = append(guidance.Context, SelectionContextExperience)
	}
	if len(guidance.Context) == 0 {
		guidance.Context = []string{SelectionContextNone}
	}
	catalog, err := stackkitrelease.ResolveUseCaseCatalog()
	if err != nil {
		guidance.Context = append(guidance.Context, "catalog_unavailable")
		return guidance
	}

	if wantsPreselect {
		guidance.Preselect, guidance.Withheld = preselectGoals(request, catalog, workers, experience)
		selected = guidance.Preselect
	}
	guidance.Tip = selectionTip(selected, request, catalog, workers)
	return guidance
}

func onlineWorkers(inventory WizardRecommendationInventory) []core.Worker {
	workers := make([]core.Worker, 0, len(inventory.Workers))
	for _, worker := range inventory.Workers {
		switch strings.ToLower(strings.TrimSpace(worker.Status)) {
		case "offline", "error", "disabled":
			continue
		}
		if worker.Capabilities.RAM > 0 {
			workers = append(workers, worker)
		}
	}
	return workers
}

func preselectGoals(request core.WizardRecommendationRequest, catalog stackkitrelease.UseCaseCatalog, workers []core.Worker, experience int) ([]string, []core.WizardSelectionWithheld) {
	available := request.AvailableGoals
	order := map[string]int{}
	for index, goal := range selectionPriority {
		order[goal] = index
	}
	ranked := append([]string(nil), available...)
	sort.SliceStable(ranked, func(i, j int) bool {
		a, aok := order[ranked[i]]
		b, bok := order[ranked[j]]
		if !aok {
			a = len(order)
		}
		if !bok {
			b = len(order)
		}
		return a < b
	})
	chosen := map[string]bool{}
	withheld := []core.WizardSelectionWithheld{}
	allocations := map[string]stackkitrelease.UseCaseResources{}
	for _, goal := range ranked {
		if experience >= 0 && experience <= 4 && selectionAdvanced[goal] {
			withheld = append(withheld, core.WizardSelectionWithheld{Goal: goal, Code: SelectionWithheldAdvanced})
			continue
		}
		// Without inventory retain the declared offered choices; sizing is
		// guidance and cannot invent hardware evidence for the operator.
		if len(workers) == 0 {
			chosen[goal] = true
			continue
		}
		footprints, err := selectedFootprints(catalog, []string{goal}, request.ComputeProfile, request.UseCaseSettings, request.UseCaseCapabilities)
		if err != nil || !reserveFootprints(footprints, workers, allocations) {
			code := SelectionWithheldCapacity
			if goal == "ai" {
				code = SelectionWithheldLocalAI
			}
			withheld = append(withheld, core.WizardSelectionWithheld{Goal: goal, Code: code})
			continue
		}
		chosen[goal] = true
	}
	preselect := make([]string, 0, len(chosen))
	for _, goal := range available {
		if chosen[goal] {
			preselect = append(preselect, goal)
		}
	}
	return preselect, withheld
}

// selectionTip returns at most one tip: local AI hardware first, then device
// count. Without inventory it sizes against the reference device.
func selectionTip(selected []string, request core.WizardRecommendationRequest, catalog stackkitrelease.UseCaseCatalog, workers []core.Worker) *core.WizardSelectionTip {
	if len(selected) < 2 {
		return nil
	}
	footprints, err := selectedFootprints(catalog, selected, request.ComputeProfile, request.UseCaseSettings, request.UseCaseCapabilities)
	if err != nil {
		return nil
	}
	needed, maxRAM := footprintRAM(footprints), 0
	for _, worker := range workers {
		if worker.Capabilities.RAM > maxRAM {
			maxRAM = worker.Capabilities.RAM
		}
	}
	if len(workers) > 0 {
		for _, footprint := range footprints {
			if footprint.Workload == "ai" && float64(maxRAM)/1024 < footprint.HostFloor.MinRAMGB {
				return &core.WizardSelectionTip{Code: SelectionTipLocalAI}
			}
		}
	}
	perDevice, have := selectionReferenceDeviceRAMMB, len(workers)
	if maxRAM > 0 {
		perDevice = maxRAM
	}
	devices := (needed + perDevice - 1) / perDevice
	if len(selected) >= selectionFullHomelabGoals && devices < 2 {
		devices = 2
	}
	if devices > have && devices > 1 {
		return &core.WizardSelectionTip{Code: SelectionTipMoreDevices, Params: map[string]int{"devices": devices, "have": have}}
	}
	return nil
}
