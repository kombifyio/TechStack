package unifier

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
)

func TestOmittedOnTierRejectsMediaLow(t *testing.T) {
	fits := map[string]map[string]UseCaseFit{
		"photos": {"low": {Included: true, ModuleSlug: "immich-lite"}},
		"media":  {"low": {Included: false, Reason: "no lite graph"}},
	}
	omitted := OmittedOnTier([]string{"photos", "media"}, fits, "low")
	if len(omitted) != 1 || omitted[0] != "media" {
		t.Fatalf("omitted = %#v, want media", omitted)
	}
}

func TestCountAlwaysOnActive(t *testing.T) {
	fits := map[string]map[string]UseCaseFit{
		"photos": {"standard": {Included: true, Residency: "always-on", Baseline: "active-resident"}},
		"vault":  {"standard": {Included: true, Residency: "always-on", Baseline: "idle-resident"}},
	}
	if got := CountAlwaysOnActive([]string{"photos", "vault"}, fits, "standard"); got != 1 {
		t.Fatalf("always-on active = %d, want 1", got)
	}
}

func TestResolveComputeTierIgnoresPiContextAndHonorsExplicit(t *testing.T) {
	if got := resolveComputeTier(&core.KombinationSpec{Metadata: map[string]string{"context": "pi"}}, nil); got != "standard" {
		t.Fatalf("context=pi selected %q", got)
	}
	if got := resolveComputeTier(&core.KombinationSpec{Metadata: map[string]string{"compute_tier": "high", "context": "pi"}}, nil); got != "high" {
		t.Fatalf("explicit compute_tier lost: %q", got)
	}
}

func TestResolveComputeTierRecommendsLowFromRAMFloor(t *testing.T) {
	ctx := &core.DecisionContext{Environment: &core.EnvironmentInventory{
		ComputeNodes: []core.EnvironmentComputeNode{{RAMMB: 1536}},
	}}
	if got := resolveComputeTier(&core.KombinationSpec{}, ctx); got != resolverMemoryLow {
		t.Fatalf("RAM below low floor selected %q", got)
	}
}

func TestBuildDecisionTraceOmitsMediaOnLowWithoutSourceCheckout(t *testing.T) {
	t.Setenv("STACKKITS_REPO", "")
	t.Setenv("TECHSTACK_STACKKITS_DIR", "")
	t.Setenv("STACKKITS_PATH", "")
	t.Setenv("TECHSTACK_STACKKIT_USE_CASE_CATALOG", "")

	media := BuildDecisionTrace(&core.KombinationSpec{
		Metadata: map[string]string{"compute_tier": "low", "use_cases": "media"},
		Services: []core.ServiceSpec{{Name: "jellyfin", Type: "media"}},
	}, nil, nil, nil)
	if !hasBlockingUseCaseGap(media) {
		t.Fatalf("media on low produced no omitted-on-tier gap: %#v", media.BlockingGaps)
	}

	photos := BuildDecisionTrace(&core.KombinationSpec{
		Metadata: map[string]string{"compute_tier": "low", "use_cases": "photos"},
		Services: []core.ServiceSpec{{Name: "immich", Type: "photos"}},
	}, nil, nil, nil)
	if hasBlockingUseCaseGap(photos) {
		t.Fatalf("photos on low should stay admitted: %#v", photos.BlockingGaps)
	}
}

func hasBlockingUseCaseGap(trace *core.DecisionTrace) bool {
	if trace == nil {
		return false
	}
	for _, gap := range trace.BlockingGaps {
		if gap.Code == "use_case_omitted_on_compute_tier" && gap.Blocking {
			return true
		}
	}
	return false
}
