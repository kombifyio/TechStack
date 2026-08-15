package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/core"
)

func TestGenerateUnifiedDeadlineDefaultsWhenUnset(t *testing.T) {
	t.Setenv("TECHSTACK_GENERATE_UNIFIED_TIMEOUT_SECONDS", "")
	if got := generateUnifiedDeadline(); got != defaultGenerateUnifiedDeadline {
		t.Fatalf("deadline = %s, want %s", got, defaultGenerateUnifiedDeadline)
	}
}

func TestGenerateUnifiedDeadlineHonoursOperatorOverride(t *testing.T) {
	t.Setenv("TECHSTACK_GENERATE_UNIFIED_TIMEOUT_SECONDS", "45")
	if got := generateUnifiedDeadline(); got != 45*time.Second {
		t.Fatalf("deadline = %s, want 45s", got)
	}
}

func TestGenerateUnifiedDeadlineRejectsUnusableOverride(t *testing.T) {
	for _, raw := range []string{"0", "-30", "soon"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("TECHSTACK_GENERATE_UNIFIED_TIMEOUT_SECONDS", raw)
			if got := generateUnifiedDeadline(); got != defaultGenerateUnifiedDeadline {
				t.Fatalf("deadline = %s, want the default for override %q", got, raw)
			}
		})
	}
}

// This is the case that stranded the live demo stacks: generation that never
// returns. It must become a terminal, named failure instead of a job that stays
// "running" forever holding the per-stack execution claim.
func TestGenerateUnifiedSpecTimesOutOnWorkThatNeverReturns(t *testing.T) {
	released := make(chan struct{})
	t.Cleanup(func() { close(released) })

	start := time.Now()
	_, err := runUnifiedSpecWithinDeadline(context.Background(), 100*time.Millisecond, func() unifiedSpecOutcome {
		<-released // never returns while the test is running
		return unifiedSpecOutcome{}
	})
	if err == nil {
		t.Fatal("expected a deadline error from work that never returns")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("deadline did not fire promptly: %s", elapsed)
	}
	var provisionErr *ProvisionError
	if !errors.As(err, &provisionErr) {
		t.Fatalf("error = %T (%v), want *ProvisionError", err, err)
	}
	if provisionErr.Step != StepGenerateUnified {
		t.Fatalf("step = %q, want %q", provisionErr.Step, StepGenerateUnified)
	}
	if !strings.Contains(provisionErr.Message, "exceeded") {
		t.Fatalf("message = %q, want it to name the exceeded deadline", provisionErr.Message)
	}
	if provisionErr.Details == "" {
		t.Fatal("timeout must carry operator guidance, not just a bare message")
	}
}

// Work that finishes inside the deadline is returned untouched.
func TestGenerateUnifiedSpecReturnsWorkThatFinishesInTime(t *testing.T) {
	want := &core.UnifiedSpec{StackKit: "cloud-kit"}
	got, err := runUnifiedSpecWithinDeadline(context.Background(), time.Minute, func() unifiedSpecOutcome {
		return unifiedSpecOutcome{spec: want}
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Fatalf("spec = %#v, want %#v", got, want)
	}
}

// A failure from the work itself keeps its own step error rather than being
// masked by the deadline wrapper.
func TestGenerateUnifiedSpecPropagatesWorkFailure(t *testing.T) {
	inner := wrapProvisionError(StepGenerateUnified, "unification error: boom", "Could not generate final deployment spec.")
	_, err := runUnifiedSpecWithinDeadline(context.Background(), time.Minute, func() unifiedSpecOutcome {
		return unifiedSpecOutcome{err: inner}
	})
	if !errors.Is(err, inner) {
		t.Fatalf("error = %v, want the work's own failure", err)
	}
}

// Cancellation must also be terminal and named, not a silent stall.
func TestGenerateUnifiedSpecReportsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	released := make(chan struct{})
	t.Cleanup(func() { close(released) })

	_, err := runUnifiedSpecWithinDeadline(ctx, time.Hour, func() unifiedSpecOutcome {
		<-released
		return unifiedSpecOutcome{}
	})
	if err == nil {
		t.Fatal("expected a cancellation error")
	}
	var provisionErr *ProvisionError
	if !errors.As(err, &provisionErr) {
		t.Fatalf("error = %T (%v), want *ProvisionError", err, err)
	}
	if provisionErr.Step != StepGenerateUnified {
		t.Fatalf("step = %q, want %q", provisionErr.Step, StepGenerateUnified)
	}
	if !strings.Contains(provisionErr.Message, "cancelled") {
		t.Fatalf("message = %q, want it to name the cancellation", provisionErr.Message)
	}
}
