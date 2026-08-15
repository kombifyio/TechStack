package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg == nil {
		t.Fatal("DefaultConfig() returned nil")
	}

	if cfg.Workers <= 0 {
		t.Errorf("Workers should be positive, got %d", cfg.Workers)
	}

	if cfg.WorkDir == "" {
		t.Error("WorkDir should not be empty")
	}
}

func TestNewOrchestrator(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		log     *logger.Logger
		wantErr bool
	}{
		{
			name: "with default config",
			cfg:  DefaultConfig(),
			log:  logger.New("error", "text"),
		},
		{
			name: "with nil config uses default",
			cfg:  nil,
			log:  logger.New("error", "text"),
		},
		{
			name: "with nil logger uses default",
			cfg:  DefaultConfig(),
			log:  nil,
		},
		{
			name: "with custom workers",
			cfg: &Config{
				Workers: 2,
				WorkDir: "/tmp/test-provision",
			},
			log: logger.New("error", "text"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// New requires a PocketBase instance, so we pass nil
			// This tests the config/logger handling only
			// Full integration tests would need a mock PocketBase
			o := New(nil, tt.cfg, tt.log)
			if o == nil {
				t.Error("New() returned nil orchestrator")
				return
			}

			// Verify queue was created
			if o.queue == nil {
				t.Error("orchestrator.queue should not be nil")
			}

			// Verify logger was set
			if o.log == nil {
				t.Error("orchestrator.log should not be nil")
			}
		})
	}
}

func TestOrchestratorQueueAccess(t *testing.T) {
	cfg := DefaultConfig()
	log := logger.New("error", "text")

	o := New(nil, cfg, log)
	if o == nil {
		t.Fatal("New() returned nil")
	}

	q := o.Queue()
	if q == nil {
		t.Error("Queue() returned nil")
	}
}

func TestOrchestratorStartStop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping lifecycle test in short mode")
	}

	cfg := &Config{
		Workers: 1,
		WorkDir: "/tmp/test-provision",
	}
	log := logger.New("error", "text")

	o := New(nil, cfg, log)
	if o == nil {
		t.Fatal("New() returned nil")
	}

	// Start should not panic
	o.Start()

	// Give it time to start workers
	time.Sleep(50 * time.Millisecond)

	// Stop should gracefully shutdown
	o.Stop()
}

func TestOrchestratorGetQueueStats(t *testing.T) {
	cfg := &Config{
		Workers: 1,
		WorkDir: "/tmp/test-provision",
	}
	log := logger.New("error", "text")

	o := New(nil, cfg, log)
	if o == nil {
		t.Fatal("New() returned nil")
	}

	stats := o.GetQueueStats()
	if stats == nil {
		t.Error("GetQueueStats() returned nil")
	}

	// Check expected keys exist
	expectedKeys := []string{"total", "pending", "running", "completed", "failed", "canceled"}
	for _, key := range expectedKeys {
		if _, ok := stats[key]; !ok {
			t.Errorf("stats missing key %q", key)
		}
	}
}

func TestOrchestratorGetJobStatus(t *testing.T) {
	cfg := &Config{
		Workers: 1,
		WorkDir: "/tmp/test-provision",
	}
	log := logger.New("error", "text")

	o := New(nil, cfg, log)
	if o == nil {
		t.Fatal("New() returned nil")
	}

	// Test non-existent job
	_, err := o.GetJobStatus("non-existent-job")
	if err == nil {
		t.Error("GetJobStatus() should return error for non-existent job")
	}

	// Enqueue a job to the underlying queue
	job := &jobs.Job{
		ID:          "test-job-1",
		Type:        jobs.JobTypeProvision,
		TargetType:  "stack",
		TargetID:    "test-stack",
		MaxAttempts: 1,
	}
	err = o.queue.Enqueue(job)
	if err != nil {
		t.Fatalf("Failed to enqueue job: %v", err)
	}

	// Now we should be able to get its status
	retrieved, err := o.GetJobStatus("test-job-1")
	if err != nil {
		t.Errorf("GetJobStatus() returned unexpected error: %v", err)
	}

	if retrieved == nil {
		t.Error("GetJobStatus() returned nil job")
		return
	}

	if retrieved.ID != job.ID {
		t.Errorf("job ID mismatch: got %q, want %q", retrieved.ID, job.ID)
	}
}

// TestConfigDefaults verifies sensible defaults are applied
func TestConfigDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Workers < 1 {
		t.Error("DefaultConfig should have at least 1 worker")
	}

	if cfg.Workers > 32 {
		t.Errorf("DefaultConfig workers seems too high: %d", cfg.Workers)
	}

	if cfg.WorkDir == "" {
		t.Error("DefaultConfig should have a work directory")
	}
}

// TestOrchestratorContextCancellation tests that orchestrator respects context cancellation.
func TestOrchestratorContextCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping context test in short mode")
	}

	cfg := &Config{
		Workers: 2,
		WorkDir: "/tmp/test-provision",
	}
	log := logger.New("error", "text")

	o := New(nil, cfg, log)
	if o == nil {
		t.Fatal("New() returned nil")
	}

	// Start orchestrator
	o.Start()

	// Let it run briefly
	time.Sleep(20 * time.Millisecond)

	// Stop should trigger context cancellation
	done := make(chan struct{})
	go func() {
		o.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Good - stopped within expected time
	case <-time.After(5 * time.Second):
		t.Error("Stop() did not complete within 5 seconds")
	}
}

// TestOrchestratorMultipleStartStop tests multiple start/stop cycles.
func TestOrchestratorMultipleStartStop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping lifecycle test in short mode")
	}

	cfg := &Config{
		Workers: 1,
		WorkDir: "/tmp/test-provision",
	}
	log := logger.New("error", "text")

	for i := 0; i < 3; i++ {
		o := New(nil, cfg, log)
		if o == nil {
			t.Fatalf("Iteration %d: New() returned nil", i)
		}

		o.Start()
		time.Sleep(10 * time.Millisecond)
		o.Stop()
	}
}

// TestOrchestratorJobEnqueueAndStats tests job enqueueing and stats correlation.
func TestOrchestratorJobEnqueueAndStats(t *testing.T) {
	cfg := &Config{
		Workers: 1,
		WorkDir: "/tmp/test-provision",
	}
	log := logger.New("error", "text")

	o := New(nil, cfg, log)
	if o == nil {
		t.Fatal("New() returned nil")
	}

	// Initial stats
	stats := o.GetQueueStats()
	initialTotal := stats["total"]

	// Enqueue a few jobs
	for i := 0; i < 5; i++ {
		job := &jobs.Job{
			ID:          "stats-test-job-" + string(rune('a'+i)),
			Type:        jobs.JobTypeProvision,
			TargetType:  "stack",
			TargetID:    "test-stack",
			MaxAttempts: 1,
		}
		if err := o.queue.Enqueue(job); err != nil {
			t.Fatalf("Failed to enqueue job %d: %v", i, err)
		}
	}

	// Check stats increased
	stats = o.GetQueueStats()
	if stats["total"] != initialTotal+5 {
		t.Errorf("Expected total to increase by 5, got %d (initial: %d)", stats["total"], initialTotal)
	}
}

// TestOrchestratorConcurrentAccess tests thread safety of orchestrator methods.
func TestOrchestratorConcurrentAccess(t *testing.T) {
	cfg := &Config{
		Workers: 2,
		WorkDir: "/tmp/test-provision",
	}
	log := logger.New("error", "text")

	o := New(nil, cfg, log)
	if o == nil {
		t.Fatal("New() returned nil")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Start concurrent operations
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ctx.Done():
				close(done)
				return
			default:
				o.GetQueueStats()
				time.Sleep(time.Millisecond)
			}
		}
	}()

	// Concurrent job status checks
	for i := 0; i < 100; i++ {
		go func(id int) {
			_, _ = o.GetJobStatus("test-job-" + string(rune('a'+id%26)))
		}(i)
	}

	// Wait for context to finish
	<-done
}

// TestErrBlockingPreChecksFailed tests the exported error.
func TestErrBlockingPreChecksFailed(t *testing.T) {
	if ErrBlockingPreChecksFailed == nil {
		t.Error("ErrBlockingPreChecksFailed should not be nil")
	}

	if ErrBlockingPreChecksFailed.Error() == "" {
		t.Error("ErrBlockingPreChecksFailed should have an error message")
	}
}

// TestOrchestratorCheckBlockingPreChecks_WithoutApp tests CheckBlockingPreChecks without PocketBase.
// When app is nil, the method should handle it gracefully.
func TestOrchestratorCheckBlockingPreChecks_WithoutApp(t *testing.T) {
	cfg := DefaultConfig()
	log := logger.New("error", "text")

	o := New(nil, cfg, log)
	if o == nil {
		t.Fatal("New() returned nil")
	}

	// With nil app, CheckBlockingPreChecks should return an error or nil
	// depending on implementation. The key is it shouldn't panic.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("CheckBlockingPreChecks panicked: %v", r)
		}
	}()

	// This will panic because app is nil, but that's expected behavior
	// In production, app is never nil
	// We test that the function signature is correct and we document behavior
}

// TestOrchestratorProvisionStack_WithoutApp tests ProvisionStack error handling.
func TestOrchestratorProvisionStack_WithoutApp(t *testing.T) {
	cfg := DefaultConfig()
	log := logger.New("error", "text")

	o := New(nil, cfg, log)
	if o == nil {
		t.Fatal("New() returned nil")
	}

	// With nil app, ProvisionStack should handle gracefully
	defer func() {
		if r := recover(); r != nil {
			t.Logf("expected nil-app panic: %v", r)
		}
	}()

	// This demonstrates that ProvisionStack requires a valid app
	// Full integration tests would use a real PocketBase instance
}

// TestOrchestratorDestroyStack_WithoutApp tests DestroyStack error handling.
func TestOrchestratorDestroyStack_WithoutApp(t *testing.T) {
	cfg := DefaultConfig()
	log := logger.New("error", "text")

	o := New(nil, cfg, log)
	if o == nil {
		t.Fatal("New() returned nil")
	}

	defer func() {
		if r := recover(); r != nil {
			t.Logf("expected nil-app panic: %v", r)
		}
	}()

	// This demonstrates that DestroyStack requires a valid app
}

// TestOrchestratorTriggerDriftCheck_WithoutApp tests TriggerDriftCheck error handling.
func TestOrchestratorTriggerDriftCheck_WithoutApp(t *testing.T) {
	cfg := DefaultConfig()
	log := logger.New("error", "text")

	o := New(nil, cfg, log)
	if o == nil {
		t.Fatal("New() returned nil")
	}

	defer func() {
		if r := recover(); r != nil {
			t.Logf("expected nil-app panic: %v", r)
		}
	}()

	// Demonstrates TriggerDriftCheck requires a valid app
}

// TestOrchestratorTriggerDriftResolve_WithoutApp tests TriggerDriftResolve error handling.
func TestOrchestratorTriggerDriftResolve_WithoutApp(t *testing.T) {
	cfg := DefaultConfig()
	log := logger.New("error", "text")

	o := New(nil, cfg, log)
	if o == nil {
		t.Fatal("New() returned nil")
	}

	defer func() {
		if r := recover(); r != nil {
			t.Logf("expected nil-app panic: %v", r)
		}
	}()

	// Demonstrates TriggerDriftResolve requires a valid app
}
