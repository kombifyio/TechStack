package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

// StackKitCLIPrepRunner is the runtime preparation step of the pinned
// published release. There is no source-built CLI fallback.
type StackKitCLIPrepRunner struct {
	Progress func(stackKitCLIProgressEvent)
	release  *stackkitrelease.Release
}

type stackKitCLIProgressEvent struct {
	Time               time.Time         `json:"time"`
	Phase              string            `json:"phase"`
	Status             string            `json:"status"`
	Message            string            `json:"message,omitempty"`
	FailureClass       string            `json:"failure_class,omitempty"`
	FailureClassLegacy string            `json:"failureClass,omitempty"`
	Attributes         map[string]string `json:"attributes,omitempty"`
}

// NewPinnedStackKitCLIPrepRunner creates the runtime preparation runner from
// the exact published release admitted by stackkitrelease.Cache.
func NewPinnedStackKitCLIPrepRunner(release stackkitrelease.Release) *StackKitCLIPrepRunner {
	return &StackKitCLIPrepRunner{release: &release}
}

func (r *StackKitCLIPrepRunner) PrepareStackKitRuntimeTarget(_ context.Context, req RuntimeActionRequest) (*RuntimeTargetBootstrapResult, error) {
	if r == nil {
		return nil, fmt.Errorf("StackKits CLI prep runner is not configured")
	}
	target := normalizeRuntimeActionTarget(req.RuntimeTarget)
	if !runtimeActionTargetHasSSHCredential(target) {
		return nil, nil
	}
	stackSpecPath := strings.TrimSpace(req.StackSpecPath)
	if stackSpecPath == "" {
		return nil, fmt.Errorf("StackKits CLI prepare requires stack_spec_path")
	}
	stackSpecPath, err := filepath.Abs(filepath.Clean(stackSpecPath))
	if err != nil {
		return nil, fmt.Errorf("resolve stack spec path: %w", err)
	}
	if _, err := os.Stat(stackSpecPath); err != nil {
		return nil, fmt.Errorf("StackKits CLI prepare stack spec missing: %w", err)
	}
	stackKit := firstNonEmpty(strings.TrimSpace(req.StackKit), DefaultBasementKitRef)
	// prepare executes the same pinned CLI as generate, so it needs the same
	// canonical document. Handing it the persisted v1 handoff failed the
	// rollout one step after generate finally passed:
	//
	//	Managed runtime target bootstrap failed: StackKits CLI prepare failed:
	//	Error: architecturev2 migration_required: StackSpec v1 is readable only
	//	through the migration adapter and cannot enter prepare
	if r.release == nil {
		return nil, fmt.Errorf("StackKits prepare requires the pinned published release")
	}
	if _, canonicalErr := canonicalStackSpecFor(stackSpecPath, stackKit, req.StackName); canonicalErr != nil {
		return nil, canonicalErr
	}
	// The v2 line has no host preparation to run. docs/CLI.md calls prepare
	// "an optional host-conformance step" and the CLI refuses it outright
	// for a canonical document:
	//
	//	Error: prepare: canonical StackSpec v2 has no governed
	//	host-preparation implementation; use external host
	//	admission/conformance and the resolved execution channel
	//
	// Host readiness is Techstack's own concern and is covered by
	// SSHRuntimeTargetBootstrapper; running the CLI here only produced a
	// guaranteed failure, so the step reports that it does not apply
	// instead of inventing a success.
	return &RuntimeTargetBootstrapResult{
		Status:     "ready",
		ReasonCode: RuntimeTargetBootstrapNotApplicable,
		Message:    "StackKits has no governed host preparation for a canonical v2 StackSpec; host conformance is external to the CLI",
	}, nil
}
