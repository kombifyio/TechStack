package jobs

import (
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

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
