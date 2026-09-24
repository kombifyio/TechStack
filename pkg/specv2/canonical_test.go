package specv2

import (
	"context"
	"strings"
	"testing"
)

func TestRequireCanonicalV2RejectsV1WithUseCases(t *testing.T) {
	err := RequireCanonicalV2(map[string]any{
		"name":     "home",
		"stackkit": KitSlugBasement,
		"useCases": []any{"photos"},
		"nodes": []any{
			map[string]any{"name": "main", "role": "standalone"},
		},
	})
	if err == nil {
		t.Fatal("v1 StackSpec with useCases must be refused")
	}
	if !strings.Contains(err.Error(), "useCases") {
		t.Fatalf("error = %v, want it to name useCases", err)
	}
}

func TestRequireCanonicalV2AcceptsArchitectureV2(t *testing.T) {
	for _, version := range []string{"stackkit/v2alpha1", "stackkit/v2alpha2"} {
		spec := basementSeed()
		spec["apiVersion"] = version
		if err := RequireCanonicalV2(spec); err != nil {
			t.Fatalf("canonical %s seed: %v", version, err)
		}
	}
}

func TestRequireCanonicalV2RejectsV2HeaderWithLegacySSH(t *testing.T) {
	spec := basementSeed()
	spec["ssh"] = map[string]any{"user": "root", "port": 22}
	if err := RequireCanonicalV2(spec); err == nil {
		t.Fatal("v2 header plus top-level ssh must be refused")
	}
}

func TestValidateSpecRejectsV1WithUseCasesBeforeCLI(t *testing.T) {
	validator := &CLIValidator{Binary: "stackkit-not-invoked"}
	err := validator.ValidateSpec(context.Background(), map[string]any{
		"name":     "home",
		"stackkit": KitSlugBasement,
		"useCases": []any{"photos", "files"},
	})
	if err == nil {
		t.Fatal("ValidateSpec must refuse v1+useCases without invoking the CLI")
	}
	if !strings.Contains(err.Error(), "useCases") {
		t.Fatalf("error = %v, want a Techstack v2 gate, not a CLI migration report", err)
	}
}

func TestProjectRejectsV1Seed(t *testing.T) {
	_, err := project(map[string]any{
		"name":     "legacy",
		"stackkit": KitSlugBasement,
		"useCases": []any{"photos"},
		"metadata": map[string]any{"name": "legacy"},
		"nodes": []any{
			map[string]any{"id": "main", "roles": []any{"controller"}},
		},
	}, foundIntent("legacy", "photos"), "hl-1")
	if err == nil {
		t.Fatal("Project must refuse a v1 seed instead of copying useCases onto it")
	}
}

func TestMixedVersionFieldsNamesUseCasesOnV1(t *testing.T) {
	mixed := MixedVersionFields(map[string]any{"stackkit": KitSlugBasement, "useCases": []any{"vault"}})
	named := false
	for _, field := range mixed {
		if field == "useCases" {
			named = true
		}
	}
	if !named {
		t.Fatalf("MixedVersionFields = %#v, want useCases", mixed)
	}
	if got := MixedVersionFields(map[string]any{"kit": KitSlugBasement, "name": "home"}); len(got) != 0 {
		t.Fatalf("v1 kit alias reported mixed fields %#v", got)
	}
	if got := MixedVersionFields(map[string]any{"kit": map[string]any{"slug": KitSlugBasement}}); len(got) == 0 {
		t.Fatal("v2 kit mapping on a v1 document must be mixed")
	}
	if got := MixedVersionFields(basementSeed()); len(got) != 0 {
		t.Fatalf("canonical v2 reported mixed fields %#v", got)
	}
}
