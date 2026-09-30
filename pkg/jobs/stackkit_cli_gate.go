package jobs

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sync/semaphore"
)

// A StackKits CLI generation evaluates the whole CUE catalog in a subprocess
// that shares the control plane's container memory limit. Two or three
// concurrent managed rollouts in generate_iac OOM-killed the 2Gi Render
// instance and orphaned every in-flight job, so generations queue per process.
const (
	stackKitCLIConcurrencyEnv     = "TECHSTACK_STACKKIT_CLI_CONCURRENCY"
	defaultStackKitCLIConcurrency = 1
	maxStackKitCLIConcurrency     = 16
)

// stackKitCLIGate is shared by every job in the process.
var stackKitCLIGate = sync.OnceValue(func() *semaphore.Weighted {
	return semaphore.NewWeighted(int64(stackKitCLIConcurrencyFromEnvironment()))
})

func stackKitCLIConcurrencyFromEnvironment() int {
	raw := strings.TrimSpace(os.Getenv(stackKitCLIConcurrencyEnv))
	if raw == "" {
		return defaultStackKitCLIConcurrency
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 || parsed > maxStackKitCLIConcurrency {
		return defaultStackKitCLIConcurrency
	}
	return parsed
}

// acquireStackKitCLI blocks until a StackKits CLI slot is free or ctx ends.
// waiting runs once, before blocking, when another generation holds the slot.
func acquireStackKitCLI(ctx context.Context, waiting func()) (func(), error) {
	gate := stackKitCLIGate()
	if !gate.TryAcquire(1) {
		if waiting != nil {
			waiting()
		}
		if err := gate.Acquire(ctx, 1); err != nil {
			return nil, err
		}
	}
	return func() { gate.Release(1) }, nil
}
