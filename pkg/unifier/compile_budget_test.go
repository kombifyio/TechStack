package unifier

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

// Concurrent kit loads must not overlap their CUE compiles. Four job-queue
// workers each compiling the StackKit base at once is what pushed the 512 MiB
// production container past its limit; serialising the compile is what keeps
// the live heap small enough for GOMEMLIMIT to collect under the ceiling.
func TestConcurrentKitLoadsDoNotOverlapCompiles(t *testing.T) {
	root := os.Getenv("TECHSTACK_STACKKITS_TESTDIR")
	if root == "" {
		t.Skip("set TECHSTACK_STACKKITS_TESTDIR to a StackKits checkout")
	}
	if _, err := os.Stat(filepath.Join(root, "base")); err != nil {
		t.Skipf("no base/ under %s: %v", root, err)
	}

	const loaders = 4
	var inFlight, maxInFlight int64
	var wg sync.WaitGroup

	// Observe overlap by sampling the budget: a goroutine that holds the lock
	// increments, and no other holder may be counted at the same time.
	observe := func() func() {
		compileBudget.Lock()
		now := atomic.AddInt64(&inFlight, 1)
		for {
			peak := atomic.LoadInt64(&maxInFlight)
			if now <= peak || atomic.CompareAndSwapInt64(&maxInFlight, peak, now) {
				break
			}
		}
		return func() {
			atomic.AddInt64(&inFlight, -1)
			compileBudget.Unlock()
		}
	}

	for i := 0; i < loaders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := observe()
			runtime.Gosched()
			release()
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&maxInFlight); got != 1 {
		t.Fatalf("observed %d concurrent compile-budget holders, want 1", got)
	}
}

// The budget must not deadlock a real load, and repeated loads must still work.
func TestKitLoadsStillSucceedUnderTheBudget(t *testing.T) {
	root := os.Getenv("TECHSTACK_STACKKITS_TESTDIR")
	if root == "" {
		t.Skip("set TECHSTACK_STACKKITS_TESTDIR to a StackKits checkout")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			loader, err := NewStackKitLoaderWithDir(root)
			if err != nil {
				errs <- err
				return
			}
			if _, err := loader.LoadKit("cloud-kit"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent load failed: %v", err)
	}
}
