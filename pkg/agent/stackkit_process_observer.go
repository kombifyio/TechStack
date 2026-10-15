package agent

import (
	"context"
	"os/exec"
	"runtime"

	"github.com/kombifyio/techstack/pkg/agent/procfence"
)

// ProcessRef identifies a started executor process group.
type ProcessRef struct {
	PID        int
	PGID       int
	StartTicks uint64
}

type processObserverKey struct{}

// WithProcessObserver reports the executor process group right after it starts,
// so the caller can record it durably and a restarted Guard can prove the group
// is gone before it admits conflicting work.
func WithProcessObserver(ctx context.Context, observe func(ProcessRef)) context.Context {
	return context.WithValue(ctx, processObserverKey{}, observe)
}

func observeStackKitProcess(ctx context.Context, process *exec.Cmd) {
	observe, ok := ctx.Value(processObserverKey{}).(func(ProcessRef))
	if !ok || observe == nil || process.Process == nil {
		return
	}
	ref := ProcessRef{PID: process.Process.Pid}
	if runtime.GOOS != "windows" {
		// The executor is its own process group leader (Setpgid).
		ref.PGID = ref.PID
		ref.StartTicks = procfence.StartTicks(ref.PID)
	}
	observe(ref)
}

// ProcessObserverFrom returns the observer attached by WithProcessObserver. A
// remote executor uses it to report the process group it started elsewhere.
func ProcessObserverFrom(ctx context.Context) (func(ProcessRef), bool) {
	observe, ok := ctx.Value(processObserverKey{}).(func(ProcessRef))
	return observe, ok && observe != nil
}
