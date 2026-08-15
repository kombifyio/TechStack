package jobs

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/unifier"
)

// defaultGenerateUnifiedDeadline bounds StackKit artifact generation. The CUE
// loader and the unification are pure computation with no context, so a
// pathological input cannot be cancelled from the inside. Without a bound the
// job simply stops moving: state stays running, no error is ever written, and
// the per-stack execution claim is held forever, which is what stranded the
// live demo stacks.
const defaultGenerateUnifiedDeadline = 8 * time.Minute

// generateUnifiedDeadline resolves the bound, allowing an operator to widen it
// for a genuinely large kit without a rebuild.
func generateUnifiedDeadline() time.Duration {
	raw := os.Getenv("TECHSTACK_GENERATE_UNIFIED_TIMEOUT_SECONDS")
	if raw == "" {
		return defaultGenerateUnifiedDeadline
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return defaultGenerateUnifiedDeadline
	}
	return time.Duration(seconds) * time.Second
}

type unifiedSpecOutcome struct {
	spec *core.UnifiedSpec
	err  error
}

// generateUnifiedSpecWithinDeadline builds the unifier and unifies the spec
// under a hard deadline. Neither call accepts a context, so the work runs on
// its own goroutine and the deadline is enforced from the outside. A timed-out
// goroutine is deliberately left to finish on its own: it holds no lock the
// caller needs, and abandoning it converts an invisible hang into a terminal,
// named failure that frees the job and the stack.
func generateUnifiedSpecWithinDeadline(
	ctx context.Context,
	stackKitsDir string,
	spec *core.KombinationSpec,
	workers []core.Worker,
) (*core.UnifiedSpec, error) {
	return runUnifiedSpecWithinDeadline(ctx, generateUnifiedDeadline(), func() unifiedSpecOutcome {
		engine, err := unifier.NewWithStackKitDir(stackKitsDir)
		if err != nil {
			return unifiedSpecOutcome{err: wrapProvisionError(
				StepGenerateUnified,
				fmt.Sprintf("failed to create unifier: %v", err),
				"Could not initialize the configuration engine.",
			)}
		}
		unified, err := engine.Unify(spec, workers)
		if err != nil {
			return unifiedSpecOutcome{err: wrapProvisionError(
				StepGenerateUnified,
				fmt.Sprintf("unification error: %v", err),
				"Could not generate final deployment spec.",
			)}
		}
		return unifiedSpecOutcome{spec: unified}
	})
}

// runUnifiedSpecWithinDeadline is the deadline mechanism on its own so it can be
// exercised against work that genuinely never returns.
func runUnifiedSpecWithinDeadline(
	ctx context.Context,
	deadline time.Duration,
	work func() unifiedSpecOutcome,
) (*core.UnifiedSpec, error) {
	// Buffered so an abandoned goroutine can always publish and exit.
	done := make(chan unifiedSpecOutcome, 1)
	go func() { done <- work() }()

	timer := time.NewTimer(deadline)
	defer timer.Stop()

	select {
	case outcome := <-done:
		return outcome.spec, outcome.err
	case <-timer.C:
		return nil, wrapProvisionError(
			StepGenerateUnified,
			fmt.Sprintf("StackKit artifact generation exceeded %s", deadline),
			"Generating the deployment artifacts took too long and was stopped. Retry the rollout; if it times out again the StackKit inputs for this stack need review.",
		)
	case <-ctx.Done():
		return nil, wrapProvisionError(
			StepGenerateUnified,
			fmt.Sprintf("StackKit artifact generation cancelled: %v", ctx.Err()),
			"The rollout was cancelled while generating deployment artifacts.",
		)
	}
}
