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
	// image authored at build time, one directory per kit. Running
	// `stackkit init` per request would put GitHub in the request path
	// (init resolves and verifies the published release over the network),
	// so seeds are consumed from these build-time templates instead.
	SpecTemplatesEnv = "TECHSTACK_STACKKIT_SPEC_TEMPLATES"

	// specAPIVersion is the only shape the pinned CLI executes.
	specAPIVersion = "stackkit/v2alpha1"
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
	if !KnownKitSlugs[kitSlug] {
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
	if len(seed) == 0 {
		return nil, fmt.Errorf("specv2: canonical %s seed is empty", kitSlug)
	}
	apiVersion, _ := seed["apiVersion"].(string)
	if strings.TrimSpace(apiVersion) != specAPIVersion {
		return nil, fmt.Errorf("specv2: canonical %s seed has apiVersion %q, want %q", kitSlug, apiVersion, specAPIVersion)
	}
	kit, _ := seed["kit"].(map[string]any)
	slug, _ := kit["slug"].(string)
	if strings.TrimSpace(slug) != kitSlug {
		return nil, fmt.Errorf("specv2: canonical seed under %s declares kit %q", kitSlug, slug)
	}
	return seed, nil
}

var _ SeedSource = (*TemplateSeedSource)(nil)
