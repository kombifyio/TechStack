package specv2

import (
	"fmt"
	"sort"
	"strings"
)

const (
	// SpecAPIVersion remains the admitted legacy migration result identity.
	SpecAPIVersion       = "stackkit/v2alpha1"
	NativeSpecAPIVersion = "stackkit/v2alpha2"
	specKind             = "StackSpec"
)

// v1ForbiddenV2Fields are Architecture v2 top-level names the StackKits CLI
// either classifies as mixed-version or, for useCases, as unknown v1 fields
// (v1.unknown-fields). They must never appear on an unversioned document.
var v1ForbiddenV2Fields = []string{
	"access",
	"availability",
	"bridge",
	"capabilities",
	"controlPlane",
	"data",
	"deviceEnrollment",
	"generation",
	"install",
	"modules",
	"partitionPolicy",
	"routes",
	"sites",
	"source",
	"useCases",
	"workloads",
}

// v1DiscriminatorFields make a document with a v2 header fail closed as mixed.
var v1DiscriminatorFields = []string{
	"stackkit",
	"ssh",
	"context",
}

// RequireCanonicalV2 fails closed unless spec is an Architecture v2 StackSpec
// the pinned CLI can classify as v2. Call this before every StackKits CLI
// validate/generate/prepare and before joining or persisting a handoff.
func RequireCanonicalV2(spec map[string]any) error {
	if len(spec) == 0 {
		return fmt.Errorf("specv2: StackSpec is empty")
	}
	apiVersion, _ := spec["apiVersion"].(string)
	kind, _ := spec["kind"].(string)
	if !isCanonicalAPIVersion(apiVersion) {
		if mixed := MixedVersionFields(spec); len(mixed) > 0 {
			return fmt.Errorf("specv2: v1 StackSpec cannot carry Architecture v2 fields %s", strings.Join(mixed, ", "))
		}
		return fmt.Errorf("specv2: StackSpec apiVersion %q is not Architecture v2 (%s)", strings.TrimSpace(apiVersion), SpecAPIVersion)
	}
	if strings.TrimSpace(kind) != specKind {
		return fmt.Errorf("specv2: StackSpec kind %q is not %s", strings.TrimSpace(kind), specKind)
	}
	kit, _ := spec["kit"].(map[string]any)
	slug, _ := kit["slug"].(string)
	if !IsKnownKitSlug(strings.TrimSpace(slug)) {
		return fmt.Errorf("specv2: kit.slug %q is not an installable kit", strings.TrimSpace(slug))
	}
	for _, name := range v1DiscriminatorFields {
		if _, exists := spec[name]; exists {
			return fmt.Errorf("specv2: canonical v2 StackSpec must not contain top-level %s", name)
		}
	}
	return nil
}

// DropBindingRejectedFields removes fields the pinned StackKits
// #KitSpecBinding contract rejects on an Architecture v2 StackSpec.
// Selected use cases belong in homelab intent and workloads, never on the
// operational document (CUE: value.spec.useCases field not allowed).
func DropBindingRejectedFields(spec map[string]any) {
	if spec == nil {
		return
	}
	delete(spec, "useCases")
}

// MixedVersionFields names v2-only top-level fields on a document that is not
// canonical Architecture v2. Empty when the document is already v2 or is a
// clean v1 shape.
func MixedVersionFields(spec map[string]any) []string {
	if spec == nil {
		return nil
	}
	if apiVersion, _ := spec["apiVersion"].(string); isCanonicalAPIVersion(apiVersion) {
		return nil
	}
	mixed := make([]string, 0, 4)
	for _, name := range v1ForbiddenV2Fields {
		if _, exists := spec[name]; exists {
			mixed = append(mixed, name)
		}
	}
	// v2 `kit` is a mapping (`kit.slug`). A string `kit` is the v1/core
	// alias that the handoff writer moves to `stackkit` before the CLI sees it.
	if _, isV2Kit := spec["kit"].(map[string]any); isV2Kit {
		mixed = append(mixed, "kit")
	}
	sort.Strings(mixed)
	return mixed
}

func isCanonicalAPIVersion(version string) bool {
	switch strings.TrimSpace(version) {
	case SpecAPIVersion, NativeSpecAPIVersion:
		return true
	default:
		return false
	}
}
