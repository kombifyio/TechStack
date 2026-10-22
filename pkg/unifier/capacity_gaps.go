package unifier

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/core"
)

// capacityGaps reads the CPU, RAM and disk of the connected compute nodes,
// not only their status: a node that has not reported its hardware is not
// yet sized against, and a selected use case whose released host floor no
// reported node meets is named with the numbers that decide it.
func capacityGaps(spec *core.KombinationSpec, inventory *core.EnvironmentInventory) []core.EnvironmentGap {
	if inventory == nil {
		return nil
	}
	var reported []core.EnvironmentComputeNode
	unreported := 0
	for _, node := range inventory.ComputeNodes {
		switch node.Status {
		case "pending-registration", "missing", "offline", "planned", failureStatus:
			continue
		}
		if node.CPU <= 0 || node.RAMMB <= 0 || node.DiskGB <= 0 {
			unreported++
			continue
		}
		reported = append(reported, node)
	}
	var gaps []core.EnvironmentGap
	if unreported > 0 {
		gaps = append(gaps, core.EnvironmentGap{
			Code:     "compute_node_capacity_unreported",
			Message:  fmt.Sprintf("%d connected server(s) have not reported CPU, memory and disk yet; kombify sizes the homelab once they do.", unreported),
			Severity: "requirement",
			Source:   "environment-inventory",
		})
	}
	if len(reported) == 0 || spec == nil {
		return gaps
	}
	goals := selectedUseCaseRefs(spec.Metadata, nil)
	if len(goals) == 0 {
		return gaps
	}
	catalog, err := stackkitrelease.ResolveUseCaseCatalog()
	if err != nil {
		return gaps
	}
	profile := metadataValue(spec, "compute_tier", "standard")
	largest := core.EnvironmentComputeNode{}
	for _, node := range reported {
		if node.RAMMB > largest.RAMMB {
			largest = node
		}
	}
	var loaded []loadedGoalFootprints
	var all []stackkitrelease.WorkloadFootprint
	for _, goal := range goals {
		footprints, err := selectedFootprints(catalog, []string{goal}, profile, nil, nil)
		if err != nil {
			continue
		}
		loaded = append(loaded, loadedGoalFootprints{id: goal, footprints: footprints})
		all = append(all, footprints...)
	}
	selectionNeed := selectionStorageNeed(all)
	computeGaps := map[string]string{}
	diskGaps := map[string]string{}
	for _, item := range loaded {
		for _, footprint := range item.footprints {
			if floorMetByAny(footprint.HostFloor, reported, selectionNeed) {
				continue
			}
			if cpuAndRAMMetByAny(footprint.HostFloor, reported) {
				diskGaps[item.id] = externalStorageGapMessage(item.id, footprint.HostFloor, selectionNeed, largestCapableDiskGB(footprint.HostFloor, reported))
				continue
			}
			computeGaps[item.id] = fmt.Sprintf("%s needs a server with at least %s CPU cores, %s GB of memory and %s GB of disk; the largest connected server has %d cores, %s GB and %d GB.",
				item.id, trimNumber(footprint.HostFloor.MinCPUCores), trimNumber(footprint.HostFloor.MinRAMGB), trimNumber(footprint.HostFloor.MinStorageGB),
				largest.CPU, trimNumber(float64(largest.RAMMB)/1024), largest.DiskGB)
		}
	}
	for goal := range computeGaps {
		delete(diskGaps, goal)
	}
	gaps = append(gaps, requirementGaps("use_case_host_floor_unmet", "stackkits-release-footprints", computeGaps)...)
	gaps = append(gaps, requirementGaps("external_storage_required", "environment-inventory", diskGaps)...)
	return gaps
}

type loadedGoalFootprints struct {
	id         string
	footprints []stackkitrelease.WorkloadFootprint
}

func floorMetByAny(floor stackkitrelease.UseCaseHostFloor, nodes []core.EnvironmentComputeNode, selectionNeed float64) bool {
	for _, node := range nodes {
		if float64(node.CPU) >= floor.MinCPUCores && float64(node.RAMMB)/1024 >= floor.MinRAMGB && diskHolds(node, floor.MinStorageGB, selectionNeed) {
			return true
		}
	}
	return false
}

func cpuAndRAMMetByAny(floor stackkitrelease.UseCaseHostFloor, nodes []core.EnvironmentComputeNode) bool {
	for _, node := range nodes {
		if float64(node.CPU) >= floor.MinCPUCores && float64(node.RAMMB)/1024 >= floor.MinRAMGB {
			return true
		}
	}
	return false
}

// largestCapableDiskGB is the biggest internal disk among nodes that meet
// this footprint's CPU and RAM. A large disk on a node that cannot run the
// use case is not the disk the message asks the customer to extend.
func largestCapableDiskGB(floor stackkitrelease.UseCaseHostFloor, nodes []core.EnvironmentComputeNode) int {
	largest := 0
	for _, node := range nodes {
		if float64(node.CPU) >= floor.MinCPUCores && float64(node.RAMMB)/1024 >= floor.MinRAMGB && node.DiskGB > largest {
			largest = node.DiskGB
		}
	}
	return largest
}

func externalStorageGapMessage(goal string, floor stackkitrelease.UseCaseHostFloor, selectionNeed float64, diskGB int) string {
	need := selectionNeed
	if floor.MinStorageGB > need {
		need = floor.MinStorageGB
	}
	if need > floor.MinStorageGB {
		return fmt.Sprintf("%s needs %s GB of package data, and the selected use cases together need %s GB. The largest connected internal disk has %d GB. Connect an ext4, xfs, or btrfs drive with at least %s GB free. kombify does not format the drive.",
			goal, trimNumber(floor.MinStorageGB), trimNumber(need), diskGB, trimNumber(need))
	}
	return fmt.Sprintf("%s needs %s GB of package data. The largest connected internal disk has %d GB. Connect an ext4, xfs, or btrfs drive with at least %s GB free. kombify does not format the drive.",
		goal, trimNumber(need), diskGB, trimNumber(need))
}

func requirementGaps(code, source string, messages map[string]string) []core.EnvironmentGap {
	ids := make([]string, 0, len(messages))
	for goal := range messages {
		ids = append(ids, goal)
	}
	sort.Strings(ids)
	gaps := make([]core.EnvironmentGap, 0, len(ids))
	for _, goal := range ids {
		gaps = append(gaps, core.EnvironmentGap{
			Code: code, Message: messages[goal], Severity: "requirement", Blocking: false, Source: source,
		})
	}
	return gaps
}

func trimNumber(value float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(math.Round(value*100)/100, 'f', -1, 64), ".0")
}
