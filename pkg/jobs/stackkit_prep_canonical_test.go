package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

// prepare executes the same pinned CLI as generate, so it needs the same
// canonical document. Handing it the persisted v1 handoff failed the rollout
// one step after generate finally passed, live on 0.6.132:
//
//	Managed runtime target bootstrap failed: StackKits CLI prepare failed:
//	Error: architecturev2 migration_required: StackSpec v1 is readable only
//	through the migration adapter and cannot enter prepare
func TestPrepareExecutesTheCanonicalSpecNotTheLegacyHandoff(t *testing.T) {
	writeCanonicalTemplate(t, "cloud-kit")
	handoff := writeSpec(t, "stackkit: cloud-kit\nname: demo\nnetwork:\n  domain: demo.example.test\n")

	canonical, err := canonicalStackSpecFor(handoff, "cloud-kit", "demo")
	if err != nil {
		t.Fatalf("canonicalStackSpecFor: %v", err)
	}
	if filepath.Base(canonical.Path) == filepath.Base(handoff) {
		t.Fatal("the canonical document must not be the legacy handoff itself")
	}
	if _, statErr := os.Stat(canonical.Path); statErr != nil {
		t.Fatalf("canonical document is not on disk: %v", statErr)
	}

	// The prepare argv must name the canonical document, for whichever caller
	// still runs it. Anything else hands the CLI a v1 spec it refuses outright.
	args := stackKitPrepareArgs(
		filepath.Dir(handoff),
		filepath.Base(canonical.Path),
		&RuntimeActionTarget{Host: "203.0.113.20", User: "root", Port: 22},
		"/tmp/id_stackkit",
	)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, canonicalStackSpecFilename) {
		t.Fatalf("prepare argv = %q, want it to name %s", joined, canonicalStackSpecFilename)
	}
}

// The v2 line has no host preparation to run at all:
//
//	Error: prepare: canonical StackSpec v2 has no governed host-preparation
//	implementation; use external host admission/conformance and the resolved
//	execution channel
//
// docs/CLI.md calls prepare "an optional host-conformance step". Running it
// against a canonical document is a guaranteed failure, so the pinned runner
// reports that it does not apply -- and says so rather than claiming a
// readiness it never checked.
func TestPinnedPrepareReportsThatV2HasNoGovernedHostPreparation(t *testing.T) {
	writeCanonicalTemplate(t, "cloud-kit")
	handoff := writeSpec(t, "stackkit: cloud-kit\nname: demo\nnetwork:\n  domain: demo.example.test\n")

	runner := &StackKitCLIPrepRunner{release: &stackkitrelease.Release{}}
	result, err := runner.PrepareStackKitRuntimeTarget(t.Context(), RuntimeActionRequest{
		StackSpecPath: handoff,
		StackKit:      "cloud-kit",
		StackName:     "demo",
		RuntimeTarget: &RuntimeActionTarget{
			Host: "203.0.113.20", User: "root", Port: 22, PrivateKey: "test-private-key",
		},
	})
	if err != nil {
		t.Fatalf("PrepareStackKitRuntimeTarget: %v", err)
	}
	if result == nil {
		t.Fatal("expected a result rather than a skipped step")
	}
	if result.ReasonCode != RuntimeTargetBootstrapNotApplicable {
		t.Fatalf("ReasonCode = %q, want %q", result.ReasonCode, RuntimeTargetBootstrapNotApplicable)
	}
	if result.Status != "ready" {
		t.Fatalf("Status = %q, want ready so the rollout proceeds", result.Status)
	}
}
