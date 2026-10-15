package unifier

import (
	"reflect"
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
)

func TestGuideWizardSelectionPreselectFollowsKnownContext(t *testing.T) {
	available := []string{"photos", "files", "ai", "dev", "media"}
	request := core.WizardRecommendationRequest{SelectionIntent: "all", AvailableGoals: available}

	none := GuideWizardSelection(request, WizardRecommendationEvaluation{})
	if !reflect.DeepEqual(none.Preselect, available) || len(none.Withheld) != 0 {
		t.Fatalf("without context every available goal is selected, got %+v", none)
	}

	small := core.Worker{ID: "w1", Status: "online"}
	small.Capabilities.RAM = 8192
	known := GuideWizardSelection(request, WizardRecommendationEvaluation{
		Inventory:      WizardRecommendationInventory{Workers: []core.Worker{small}, State: "fresh"},
		SelfDisclosure: &core.OperatorSelfDisclosure{Experience: core.SelfDisclosureExperienceGuided},
	})
	if reflect.DeepEqual(known.Preselect, available) {
		t.Fatalf("a guided operator with one 8 GB device must not get everything, got %+v", known.Preselect)
	}
	for _, goal := range known.Preselect {
		if goal == "ai" || goal == "dev" {
			t.Fatalf("%s does not fit this context, got %+v", goal, known)
		}
	}
	if len(known.Withheld) == 0 {
		t.Fatal("withheld goals must be explained")
	}
}

func TestGuideWizardSelectionTipOnlyForRealSelections(t *testing.T) {
	one := GuideWizardSelection(core.WizardRecommendationRequest{Goals: []string{"files"}}, WizardRecommendationEvaluation{})
	if one != nil {
		t.Fatalf("a one-card selection yields no guidance, got %+v", one)
	}

	device := core.Worker{ID: "w1", Status: "online"}
	device.Capabilities.RAM = 8192
	many := GuideWizardSelection(core.WizardRecommendationRequest{
		Goals: []string{"files", "photos", "vault", "mail", "media", "ai"},
	}, WizardRecommendationEvaluation{Inventory: WizardRecommendationInventory{Workers: []core.Worker{device}, State: "fresh"}})
	if many == nil || many.Tip == nil || many.Tip.Code != SelectionTipLocalAI {
		t.Fatalf("an 8 GB device cannot run local AI, got %+v", many)
	}
}
