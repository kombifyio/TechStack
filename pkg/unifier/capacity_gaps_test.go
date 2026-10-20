package unifier

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
)

// Gap evaluation reads CPU, RAM and disk: an unreported node is named, and a
// released host floor no reported node meets becomes a requirement.
func TestCapacityGapsReadNodeCapacity(t *testing.T) {
	spec := &core.KombinationSpec{Metadata: map[string]string{"use_cases": "photos"}}
	inventory := &core.EnvironmentInventory{ComputeNodes: []core.EnvironmentComputeNode{
		{ID: "small", Status: "online", CPU: 2, RAMMB: 4096, DiskGB: 64},
		{ID: "fresh", Status: "online"},
	}}
	codes := map[string]bool{}
	for _, gap := range capacityGaps(spec, inventory) {
		codes[gap.Code] = true
	}
	if !codes["compute_node_capacity_unreported"] || !codes["use_case_host_floor_unmet"] {
		t.Fatalf("got %v", codes)
	}
	inventory.ComputeNodes[0].RAMMB = 8192
	for _, gap := range capacityGaps(spec, inventory) {
		if gap.Code == "use_case_host_floor_unmet" {
			t.Fatalf("an 8 GB node meets the photos floor: %s", gap.Message)
		}
	}
}
