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
	unmet := map[string]string{}
	for _, goal := range goals {
		footprints, err := selectedFootprints(catalog, []string{goal}, profile, nil, nil)
		if err != nil {
			continue
		}
		for _, footprint := range footprints {
			if floorMetByAny(footprint.HostFloor, reported) {
				continue
			}
			unmet[goal] = fmt.Sprintf("%s needs a server with at least %s CPU cores, %s GB of memory and %s GB of disk; the largest connected server has %d cores, %s GB and %d GB.",
				goal, trimNumber(footprint.HostFloor.MinCPUCores), trimNumber(footprint.HostFloor.MinRAMGB), trimNumber(footprint.HostFloor.MinStorageGB),
				largest.CPU, trimNumber(float64(largest.RAMMB)/1024), largest.DiskGB)
		}
	}
	ids := make([]string, 0, len(unmet))
	for goal := range unmet {
		ids = append(ids, goal)
	}
	sort.Strings(ids)
	for _, goal := range ids {
		gaps = append(gaps, core.EnvironmentGap{
			Code: "use_case_host_floor_unmet", Message: unmet[goal], Severity: "requirement", Source: "stackkits-release-footprints",
		})
	}
	return gaps
}

func floorMetByAny(floor stackkitrelease.UseCaseHostFloor, nodes []core.EnvironmentComputeNode) bool {
	for _, node := range nodes {
		if float64(node.CPU) >= floor.MinCPUCores && float64(node.RAMMB)/1024 >= floor.MinRAMGB && float64(node.DiskGB) >= floor.MinStorageGB {
			return true
		}
	}
	return false
}

func trimNumber(value float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(math.Round(value*100)/100, 'f', -1, 64), ".0")
}
