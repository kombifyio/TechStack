package unifier

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/specv2"
)

// selectedFootprints projects the very same choices used by release authoring.
// Capability workload references are catalog facts, never a service-name table.
func selectedFootprints(catalog stackkitrelease.UseCaseCatalog, goals []string, profile string, settings map[string]map[string]any, capabilities map[string]map[string]string) ([]stackkitrelease.WorkloadFootprint, error) {
	intent := specv2.WizardIntent{Goals: goals, UseCaseSettings: map[string]map[string]any{}, UseCaseCapabilities: capabilities}
	for _, goal := range goals {
		useCase, found := catalog.FindUseCase(goal)
		if !found {
			return nil, fmt.Errorf("release %s declares no use case %q", catalog.Release.Tag, goal)
		}
		intent.UseCaseSettings[goal] = settings[goal]
		for id, value := range settings[goal] {
			setting, found := useCase.FindSetting(id)
			if !found || !setting.AcceptsSettingValue(value) {
				return nil, fmt.Errorf("release %s rejects setting %s.%s", catalog.Release.Tag, goal, id)
			}
		}
	}
	selection, err := specv2.GoalSelectionFromIntent(intent, catalog)
	if err != nil {
		return nil, err
	}
	choices := map[string]specv2.WorkloadChoice{}
	accelerators := map[string]string{}
	for _, goal := range goals {
		choice := selection.Alternatives[goal]
		choice.Goal, choice.Workload = goal, goal
		choices[goal] = choice
		useCase, _ := catalog.FindUseCase(goal)
		if setting, found := useCase.FindSetting("accelerator"); found {
			accelerators[goal], _ = setting.Default.(string)
			if value, set := settings[goal][setting.ID]; set {
				accelerators[goal], _ = value.(string)
			}
		}
		for id := range capabilities[goal] {
			if _, found := useCase.FindCapability(id); !found {
				return nil, fmt.Errorf("release %s declares no capability %s.%s", catalog.Release.Tag, goal, id)
			}
		}
		for _, capability := range useCase.Capabilities {
			value, explicit := capabilities[goal][capability.ID]
			if !explicit {
				if !capability.Required && !capability.EnabledByDefault {
					continue
				}
				value = capability.Default.ID
			}
			if err := capability.AcceptsSelection(value); err != nil {
				return nil, err
			}
			if value == "off" {
				continue
			}
			option := capability.Default
			if capability.Alternative != nil && capability.Alternative.ID == value {
				option = *capability.Alternative
			}
			if option.WorkloadRef == "" || len(option.AlternativeRefs) > 0 {
				continue
			}
			// The default capability of a primary workload must not replace a
			// backend explicitly selected by the operator.
			if option.WorkloadRef == goal && !explicit && choice.Alternative != "" {
				continue
			}
			choices[option.WorkloadRef] = specv2.WorkloadChoice{Goal: goal, Workload: option.WorkloadRef, Alternative: option.AlternativeRef}
		}
	}
	for _, choice := range selection.AddOns {
		choices[choice.Workload] = choice
	}
	ids := make([]string, 0, len(choices))
	for id := range choices {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var result []stackkitrelease.WorkloadFootprint
	for _, id := range ids {
		choice := choices[id]
		facts, err := catalog.WorkloadFootprints(id, choice.Alternative, profile, accelerators[choice.Goal])
		if err != nil {
			return nil, err
		}
		result = append(result, facts...)
	}
	return result, nil
}

// footprintFits checks floors against the whole host and reservations against
// unallocated capacity. Missing measurements are explicit, not zero capacity.
func footprintFits(footprint stackkitrelease.WorkloadFootprint, worker core.Worker, allocated stackkitrelease.UseCaseResources) (shortfall, unknown []string) {
	floor, reservation, capacity := footprint.HostFloor, footprint.Reservation, worker.Capabilities
	check := func(name string, total, used, minimum, demand float64) {
		if minimum == 0 && demand == 0 {
			return
		}
		if total <= 0 {
			unknown = append(unknown, name)
		} else if total < minimum || total-used < demand {
			shortfall = append(shortfall, name)
		}
	}
	check("cpu", float64(capacity.CPU), float64(worker.Usage.CPU)+allocated.CPUCores, floor.MinCPUCores, reservation.CPUCores)
	check("memory", float64(capacity.RAM)/1024, float64(worker.Usage.RAM)/1024+allocated.RAMGB, floor.MinRAMGB, reservation.RAMGB)
	check("storage", float64(capacity.Disk), float64(worker.Usage.Disk)+allocated.StorageGB, floor.MinStorageGB, reservation.StorageGB)
	if footprint.CPUReservationUnspecified {
		unknown = append(unknown, "cpu_reservation")
	}
	if floor.MinAMD64MicroarchitectureLevel > 0 {
		if capacity.Arch == "" {
			unknown = append(unknown, "cpu_architecture")
		} else if strings.EqualFold(capacity.Arch, "amd64") && capacity.AMD64MicroarchitectureLevel == 0 {
			unknown = append(unknown, "cpu_microarchitecture")
		} else if strings.EqualFold(capacity.Arch, "amd64") && capacity.AMD64MicroarchitectureLevel < floor.MinAMD64MicroarchitectureLevel {
			shortfall = append(shortfall, "cpu_microarchitecture")
		}
	}
	if gpu := footprint.Accelerator; gpu != nil {
		switch {
		case capacity.GPU == nil:
			shortfall = append(shortfall, "accelerator")
		case capacity.GPU.Vendor == "":
			unknown = append(unknown, "accelerator_vendor")
		case !strings.EqualFold(gpu.Vendor, capacity.GPU.Vendor):
			shortfall = append(shortfall, "accelerator")
		default:
			if gpu.MinDriverMajor > 0 {
				if capacity.GPU.DriverMajor == 0 {
					unknown = append(unknown, "accelerator_driver")
				} else if capacity.GPU.DriverMajor < gpu.MinDriverMajor {
					shortfall = append(shortfall, "accelerator_driver")
				}
			}
		}
	}
	return shortfall, unknown
}

func footprintRAM(footprints []stackkitrelease.WorkloadFootprint) int {
	reservation, floor := 0.0, 0.0
	for _, footprint := range footprints {
		reservation += footprint.Reservation.RAMGB
		floor = math.Max(floor, footprint.HostFloor.MinRAMGB)
	}
	return int(math.Ceil(math.Max(floor, reservation) * 1024))
}

func catalogCapacityReasons(request core.WizardRecommendationRequest, inventory WizardRecommendationInventory) ([]core.WizardRecommendationReason, bool) {
	if len(request.Goals) == 0 {
		return nil, false
	}
	catalog, err := stackkitrelease.ResolveUseCaseCatalog()
	if err != nil {
		return []core.WizardRecommendationReason{{Code: "catalog_footprint_unavailable", Message: err.Error(), Source: "stackkits-release-footprints"}}, true
	}
	footprints, err := selectedFootprints(catalog, request.Goals, request.ComputeProfile, request.UseCaseSettings, request.UseCaseCapabilities)
	source := "stackkits-release-footprints:" + catalog.Release.Tag
	if err != nil {
		return []core.WizardRecommendationReason{{Code: "catalog_footprint_unavailable", Message: err.Error(), Source: source}}, true
	}
	workers := onlineWorkers(inventory)
	if len(workers) == 0 {
		return nil, false // The existing inventory status describes this state.
	}
	var reasons []core.WizardRecommendationReason
	unknown := false
	for _, footprint := range footprints {
		fits, measured := false, false
		var shortfalls, missing []string
		for _, worker := range workers {
			shortfall, absent := footprintFits(footprint, worker, stackkitrelease.UseCaseResources{})
			if len(shortfall) == 0 {
				fits = true
				if len(absent) == 0 {
					measured = true
					break
				}
			}
			for _, key := range shortfall {
				shortfalls = appendUniqueString(shortfalls, key)
			}
			for _, key := range absent {
				missing = appendUniqueString(missing, key)
			}
		}
		if !fits {
			reasons = append(reasons, core.WizardRecommendationReason{Code: "module_capacity_shortfall", Message: fmt.Sprintf("%s/%s profile %s needs more %s than an online worker provides", footprint.Workload, footprint.Module, footprint.ComputeProfile, strings.Join(shortfalls, ", ")), Source: source})
		}
		if !measured && len(missing) > 0 {
			unknown = true
			reasons = append(reasons, core.WizardRecommendationReason{Code: "module_capacity_unverified", Message: footprint.Module + ": missing capacity evidence for " + strings.Join(missing, ", "), Source: source})
		}
	}
	if !reserveFootprints(catalog, footprints, workers, map[string]stackkitrelease.UseCaseResources{}) {
		reasons = append(reasons, core.WizardRecommendationReason{Code: "selection_capacity_shortfall", Message: "Selected released module reservations do not fit the unallocated worker capacity", Source: source})
	}
	return reasons, unknown
}
