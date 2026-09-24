// Package routes provides tests for health API routes.
package routes

import (
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/config"
)

func TestNormalizeRuntimeIdentity(t *testing.T) {
	edition, mode := normalizeRuntimeIdentity(config.EditionPreview, config.ModeSelfHosted)
	if edition != config.EditionPreview || mode != config.ModeSaaS {
		t.Fatalf("preview identity = (%q, %q), want (%q, %q)", edition, mode, config.EditionPreview, config.ModeSaaS)
	}

	edition, mode = normalizeRuntimeIdentity("", config.ModeSaaS)
	if edition != config.EditionSaaSStandalone || mode != config.ModeSaaS {
		t.Fatalf("legacy saas identity = (%q, %q), want (%q, %q)", edition, mode, config.EditionSaaSStandalone, config.ModeSaaS)
	}

	edition, mode = runtimeIdentityFields(nil)
	if edition != config.EditionSelfHostOSS || mode != config.ModeSelfHosted {
		t.Fatalf("nil deps identity = (%q, %q), want (%q, %q)", edition, mode, config.EditionSelfHostOSS, config.ModeSelfHosted)
	}
}

func TestNormalizedBuildRevisionRequiresFullSHA(t *testing.T) {
	const sha = "02fa3578e0e6f74362d4208023a241cf4d2434ac"
	if got := normalizedBuildRevision(strings.ToUpper(sha)); got != sha {
		t.Fatalf("normalizedBuildRevision(full SHA) = %q, want %q", got, sha)
	}
	for _, invalid := range []string{"", "dev", "deadbeef", sha[:39], "g" + sha[1:]} {
		if got := normalizedBuildRevision(invalid); got != "" {
			t.Fatalf("normalizedBuildRevision(%q) = %q, want empty", invalid, got)
		}
	}
}
