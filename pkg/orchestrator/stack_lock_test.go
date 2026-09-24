package orchestrator

import (
	"testing"
	"time"
)

func TestStackLockTableSerializesSameStackAndAllowsOthers(t *testing.T) {
	var table stackLockTable
	holding := make(chan struct{})
	releaseFirst := make(chan struct{})

	go func() {
		unlock := table.lock("stack-a")
		close(holding)
		<-releaseFirst
		unlock()
	}()
	<-holding

	// An unrelated stack must not wait for stack-a.
	otherAcquired := make(chan struct{})
	go func() {
		unlock := table.lock("stack-b")
		close(otherAcquired)
		unlock()
	}()
	select {
	case <-otherAcquired:
	case <-time.After(2 * time.Second):
		t.Fatal("an unrelated stack was blocked by the stack-a lock")
	}

	// The same stack must wait for the current holder.
	sameAcquired := make(chan struct{})
	go func() {
		unlock := table.lock("stack-a")
		close(sameAcquired)
		unlock()
	}()
	select {
	case <-sameAcquired:
		t.Fatal("the same stack acquired the lock while it was held")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseFirst)
	select {
	case <-sameAcquired:
	case <-time.After(2 * time.Second):
		t.Fatal("the same stack lock was not released")
	}
}

func TestWorkerCountFromEnvironment(t *testing.T) {
	// The default must stay four; deployments opt into more explicitly.
	t.Setenv("TECHSTACK_JOB_WORKERS", "")
	if got := workerCountFromEnvironment(); got != 4 {
		t.Fatalf("default workers = %d, want 4", got)
	}
	t.Setenv("TECHSTACK_JOB_WORKERS", "8")
	if got := workerCountFromEnvironment(); got != 8 {
		t.Fatalf("configured workers = %d, want 8", got)
	}
	for _, invalid := range []string{"0", "-3", "many", "1000"} {
		t.Setenv("TECHSTACK_JOB_WORKERS", invalid)
		if got := workerCountFromEnvironment(); got != 4 {
			t.Fatalf("workers for %q = %d, want the safe default 4", invalid, got)
		}
	}
}
