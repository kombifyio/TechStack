package routes

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/features"
)

func TestFeatureFlagsResponse_GroupsKnownCategories(t *testing.T) {
	response := featureFlagsResponse(map[features.Category][]features.FlagState{
		features.CategorySecurity: {{Key: "network_discovery"}},
		features.CategoryBeta:     {{Key: "monthly_runtime"}},
		features.CategoryUX:       {{Key: "dark_mode"}},
	})

	if response.Security[0].Key != "network_discovery" {
		t.Fatalf("security flags = %+v", response.Security)
	}
	if response.Beta[0].Key != "monthly_runtime" {
		t.Fatalf("beta flags = %+v", response.Beta)
	}
	if response.UX[0].Key != "dark_mode" {
		t.Fatalf("ux flags = %+v", response.UX)
	}
}
