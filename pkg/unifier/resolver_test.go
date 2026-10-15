package unifier

import (
	"path/filepath"
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
)

// Availability must not be invented when the configured release authority
// fails or a consumer supplies an explicit empty catalog.
func TestStackKitResolverRejectsUnavailableCatalog(t *testing.T) {
	for _, tc := range []struct {
		name             string
		kits             []string
		missingAuthority bool
	}{
		{name: "explicit empty", kits: []string{}},
		{name: "empty name", kits: []string{""}},
		{name: "cloud only", kits: []string{StackKitCloud}},
		{name: "broken override", missingAuthority: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.missingAuthority {
				t.Setenv("TECHSTACK_STACKKITS_DIR", filepath.Join(t.TempDir(), "missing"))
				t.Setenv("STACKKITS_REPO", t.TempDir())
			}
			result := NewStackKitResolver(tc.kits).Resolve(&core.KombinationSpec{Kit: StackKitBasement})
			if result.Valid {
				t.Fatalf("unavailable catalog admitted a kit: %+v", result)
			}
		})
	}
}

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

func TestStackKitResolver_Resolve_RetiredBaseKitIsInvalid(t *testing.T) {
	resolver := NewStackKitResolver([]string{"base-kit", StackKitCloud})
	result := resolver.Resolve(&core.KombinationSpec{Kit: "base-kit"})

	if result == nil {
		t.Fatal("Resolve returned nil result")
	}
	if result.Valid {
		t.Fatal("Resolve() valid = true, want retired base-kit to be invalid")
	}
}

func TestStackKitResolver_Resolve_RetiredKitIsInvalid(t *testing.T) {
	resolver := NewStackKitResolver([]string{StackKitBasement, StackKitCloud, StackKitHA, StackKitModernHomelab})
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
	resolver := NewStackKitResolver([]string{StackKitBasement, StackKitCloud, StackKitHA, StackKitModernHomelab})
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
	resolver := NewStackKitResolver([]string{StackKitBasement, StackKitCloud, StackKitHA, "custom-kit"})

	for _, tc := range []struct {
		kit   string
		valid bool
	}{
		{StackKitBasement, true},
		{StackKitCloud, true},
		{StackKitHA, false},
		{"custom-kit", false},
	} {
		if got := resolver.Resolve(&core.KombinationSpec{Kit: tc.kit}).Valid; got != tc.valid {
			t.Fatalf("Resolve(%q).Valid = %v, want %v", tc.kit, got, tc.valid)
		}
	}
}
