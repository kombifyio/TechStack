package agent

import (
	"context"
	"os"
	"testing"
	"time"
)

// Verify the same process launcher used by the privileged RIL seam stops a
// running child when its absolute authority deadline is reached.
func TestRILAbsoluteDeadlineTerminatesProcess(t *testing.T) {
	if os.Getenv("RIL_DEADLINE_HELPER") == "1" {
		time.Sleep(30 * time.Second)
		return
	}
	deadline := time.Now().Add(400 * time.Millisecond)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	child := newStackKitProcess(ctx, os.Args[0], "-test.run=^TestRILAbsoluteDeadlineTerminatesProcess$")
	child.Env = append(os.Environ(), "RIL_DEADLINE_HELPER=1")
	started := time.Now()
	err := child.Run()
	elapsed := time.Since(started)
	if err == nil || ctx.Err() != context.DeadlineExceeded || elapsed > 5*time.Second {
		t.Fatalf("deadline did not terminate child: err=%v context=%v elapsed=%v", err, ctx.Err(), elapsed)
	}
}
