package specv2

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// SpecTemplatesEnv points at the canonical v2 StackSpec documents the
	// image authored at build time, one directory per kit. Seeds remain the
	// topology base; the admitted release CLI authors only selected workload
	// entries per request from its embedded CUE authority.
	SpecTemplatesEnv = "TECHSTACK_STACKKIT_SPEC_TEMPLATES"
)

// SeedSource loads the canonical kit seed a projection starts from.
type SeedSource interface {
	Seed(kitSlug string) (map[string]any, error)
}

// TemplateSeedSource reads build-time authored seeds from
// <Root>/<kit>/stack-spec.yaml, mirroring the rollout path's template
// consumption (pkg/jobs stackKitSpecTemplateEnv).
type TemplateSeedSource struct {
	Root string
}

// NewTemplateSeedSourceFromEnv builds the seed source from the image
// environment; it returns nil when the templates root is not configured so
// callers can fail closed with a precise reason.
func NewTemplateSeedSourceFromEnv() *TemplateSeedSource {
	root := strings.TrimSpace(os.Getenv(SpecTemplatesEnv))
	if root == "" {
		return nil
	}
	return &TemplateSeedSource{Root: root}
}

func (s *TemplateSeedSource) Seed(kitSlug string) (map[string]any, error) {
	if s == nil || strings.TrimSpace(s.Root) == "" {
		return nil, fmt.Errorf("specv2: seed templates root not configured (%s)", SpecTemplatesEnv)
	}
	kitSlug = strings.TrimSpace(kitSlug)
	if !IsKnownKitSlug(kitSlug) {
		return nil, fmt.Errorf("specv2: %q is not an installable kit", kitSlug)
	}
	path := filepath.Join(filepath.Clean(s.Root), kitSlug, "stack-spec.yaml")
	data, err := os.ReadFile(path) // #nosec G304 -- root comes from the image environment and the kit is a validated slug.
	if err != nil {
		return nil, fmt.Errorf("specv2: read canonical %s seed: %w", kitSlug, err)
	}
	var seed map[string]any
	if err := yaml.Unmarshal(data, &seed); err != nil {
		return nil, fmt.Errorf("specv2: parse canonical %s seed: %w", kitSlug, err)
	}
	if err := RequireCanonicalV2(seed); err != nil {
		return nil, fmt.Errorf("specv2: canonical %s seed: %w", kitSlug, err)
	}
	kit, _ := seed["kit"].(map[string]any)
	slug, _ := kit["slug"].(string)
	if strings.TrimSpace(slug) != kitSlug {
		return nil, fmt.Errorf("specv2: canonical seed under %s declares kit %q", kitSlug, slug)
	}
	return seed, nil
}

var _ SeedSource = (*TemplateSeedSource)(nil)
