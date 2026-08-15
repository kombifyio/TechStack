package unifier

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
)

func TestStackKitResolver_Resolve_NilSpecDefaultsToBasement(t *testing.T) {
	resolver := NewStackKitResolver(nil)
	result := resolver.Resolve(&core.KombinationSpec{})

	if result == nil {
		t.Fatal("Resolve returned nil result")
	}
	if !result.Valid {
		t.Fatalf("Resolve() valid = false, warnings: %v", result.Warnings)
	}
	if result.StackKit != StackKitBasement {
		t.Fatalf("StackKit = %q, want %q", result.StackKit, StackKitBasement)
	}
}

func TestStackKitResolver_Resolve_ExplicitCloudKit(t *testing.T) {
	resolver := NewStackKitResolver(DefaultKnownStackKits())
	result := resolver.Resolve(&core.KombinationSpec{Kit: StackKitCloud})

	if result == nil {
		t.Fatal("Resolve returned nil result")
	}
	if !result.Valid {
		t.Fatalf("Resolve() valid = false, warnings: %v", result.Warnings)
	}
	if result.StackKit != StackKitCloud {
		t.Fatalf("StackKit = %q, want %q", result.StackKit, StackKitCloud)
	}
	if result.AutoSelected {
		t.Fatal("AutoSelected = true, want false")
	}
}

func TestStackKitResolver_Resolve_LegacyBaseKitAliasesToBasement(t *testing.T) {
	resolver := NewStackKitResolver([]string{"base-kit", StackKitCloud})
	result := resolver.Resolve(&core.KombinationSpec{Kit: "base-kit"})

	if result == nil {
		t.Fatal("Resolve returned nil result")
	}
	if !result.Valid {
		t.Fatalf("Resolve() valid = false, warnings: %v", result.Warnings)
	}
	if result.StackKit != StackKitBasement {
		t.Fatalf("StackKit = %q, want %q", result.StackKit, StackKitBasement)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("expected alias warning for legacy base-kit")
	}
}

func TestStackKitResolver_Resolve_RetiredKitIsInvalid(t *testing.T) {
	resolver := NewStackKitResolver([]string{"base-kit", StackKitCloud, StackKitHA, StackKitModernHomelab})
	result := resolver.Resolve(&core.KombinationSpec{Kit: StackKitHA})

	if result == nil {
		t.Fatal("Resolve returned nil result")
	}
	if result.Valid {
		t.Fatalf("Resolve() valid = true, want retired %q to be invalid", StackKitHA)
	}
}

func TestStackKitResolver_AutoSelect_SingleLocalNodeUsesBasement(t *testing.T) {
	resolver := NewStackKitResolver(nil)
	result := resolver.Resolve(&core.KombinationSpec{
		Nodes: []core.NodeSpec{{Name: "node1", Type: "main", Provider: "local"}},
	})

	if result == nil {
		t.Fatal("Resolve returned nil result")
	}
	if !result.AutoSelected {
		t.Fatal("AutoSelected = false, want true")
	}
	if result.StackKit != StackKitBasement {
		t.Fatalf("StackKit = %q, want %q", result.StackKit, StackKitBasement)
	}
}

func TestStackKitResolver_AutoSelect_CloudProviderUsesCloudKit(t *testing.T) {
	for _, provider := range []string{"aws", "centron", "ionos"} {
		t.Run(provider, func(t *testing.T) {
			resolver := NewStackKitResolver(nil)
			result := resolver.Resolve(&core.KombinationSpec{
				Nodes: []core.NodeSpec{{Name: "cloud1", Type: "main", Provider: provider}},
			})

			if result == nil {
				t.Fatal("Resolve returned nil result")
			}
			if result.StackKit != StackKitCloud {
				t.Fatalf("StackKit = %q, want %q", result.StackKit, StackKitCloud)
			}
		})
	}
}

func TestStackKitResolver_AutoSelect_MixedLocalCloudUsesCloudKit(t *testing.T) {
	resolver := NewStackKitResolver([]string{"base-kit", StackKitCloud, StackKitHA, StackKitModernHomelab})
	result := resolver.Resolve(&core.KombinationSpec{
		Nodes: []core.NodeSpec{
			{Name: "local1", Type: "main", Provider: "local"},
			{Name: "cloud1", Type: "worker", Provider: "hetzner"},
		},
	})

	if result == nil {
		t.Fatal("Resolve returned nil result")
	}
	if result.StackKit != StackKitCloud {
		t.Fatalf("StackKit = %q, want %q", result.StackKit, StackKitCloud)
	}
}

func TestStackKitResolver_FiltersAvailableKitsToSupportedProductKits(t *testing.T) {
	resolver := NewStackKitResolver([]string{"base-kit", StackKitCloud, StackKitHA, "custom-kit"})

	if !resolver.IsKitAvailable(StackKitBasement) {
		t.Fatalf("%q should be available through legacy base-kit alias", StackKitBasement)
	}
	if !resolver.IsKitAvailable(StackKitCloud) {
		t.Fatalf("%q should be available", StackKitCloud)
	}
	if resolver.IsKitAvailable(StackKitHA) {
		t.Fatalf("%q should not be available in the TechStack product contract", StackKitHA)
	}
	if resolver.IsKitAvailable("custom-kit") {
		t.Fatal("custom-kit should not be available in the TechStack product contract")
	}
}

func TestStackKitResolver_AddKitIgnoresUnsupportedKits(t *testing.T) {
	resolver := NewStackKitResolver([]string{StackKitBasement})

	resolver.AddKit(StackKitHA)
	resolver.AddKit(StackKitCloud)

	if resolver.IsKitAvailable(StackKitHA) {
		t.Fatalf("%q should not be added", StackKitHA)
	}
	if !resolver.IsKitAvailable(StackKitCloud) {
		t.Fatalf("%q should be added", StackKitCloud)
	}
}
