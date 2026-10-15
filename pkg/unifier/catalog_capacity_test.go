package unifier

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/core"
)

// A repin must change the central capacity decision without a defaults edit.
func TestRepinnedModuleFloorChangesWizardCapacityDecision(t *testing.T) {
	catalog, err := stackkitrelease.PinnedUseCaseCatalog()
	if err != nil {
		t.Fatal(err)
	}
	writeCatalog := func(name string) string {
		t.Helper()
		data, err := json.Marshal(map[string]any{"schemaVersion": "stackkits-use-case-catalog/v1", "release": catalog.Release, "catalog": map[string]any{"useCases": catalog.UseCases}})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	authority := NewWizardRecommendationAuthority(nil)
	input := WizardRecommendationEvaluation{
		Request:   core.WizardRecommendationRequest{Goals: []string{"files"}, AvailableGoals: []string{"files"}, SelectionIntent: "all", DeploymentLane: "self-hosted", Surface: "easy"},
		Inventory: WizardRecommendationInventory{State: "fresh", Workers: []core.Worker{{ID: "worker", Status: "online", Capabilities: core.WorkerCapabilities{CPU: 64, RAM: 262144, Disk: 2048, Arch: "amd64", AMD64MicroarchitectureLevel: 3}}}},
	}
	t.Setenv(stackkitrelease.UseCaseCatalogEnv, writeCatalog("original.json"))
	selected := func(response core.WizardRecommendationResponse) bool {
		if response.Selection != nil {
			for _, goal := range response.Selection.Preselect {
				if goal == "files" {
					return true
				}
			}
		}
		return false
	}
	if !selected(authority.Recommend(input)) {
		t.Fatal("released workload must fit the disclosed worker")
	}
	useCase, found := catalog.FindUseCase("files")
	if !found {
		t.Fatal("cannot evaluate files against the pinned catalog")
	}
	for _, alternative := range useCase.Alternatives {
		if alternative.ID != useCase.DefaultAlternative {
			continue
		}
		for _, module := range alternative.Modules {
			facts, found := module.ComputeProfileFacts["standard"]
			if !found || facts.HostFloor == nil {
				t.Fatal("default module has no published standard footprint")
			}
			facts.HostFloor.MinCPUCores = float64(input.Inventory.Workers[0].Capabilities.CPU + 1)
		}
	}
	t.Setenv(stackkitrelease.UseCaseCatalogEnv, writeCatalog("repinned.json"))
	if selected(authority.Recommend(input)) {
		t.Fatal("repinned CPU floor was ignored by central capacity guidance")
	}
}
