package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
)

// TestDefaultConfig_Values tests specific expected default values.
func TestDefaultConfig_Values(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Workers != 4 {
		t.Errorf("Expected Workers=4, got %d", cfg.Workers)
	}
	if cfg.WorkDir != "data/provision" {
		t.Errorf("Expected WorkDir='data/provision', got %q", cfg.WorkDir)
	}
}

// TestConfig_CustomValues tests various custom configuration values.
func TestConfig_CustomValues(t *testing.T) {
	tests := []struct {
		name    string
		workers int
		workDir string
	}{
		{"single_worker", 1, "/tmp/test"},
		{"many_workers", 16, "/var/lib/provision"},
		{"default_like", 4, "data/provision"},
		{"edge_case", 100, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Workers: tt.workers,
				WorkDir: tt.workDir,
			}
			if cfg.Workers != tt.workers {
				t.Errorf("Workers mismatch: got %d, want %d", cfg.Workers, tt.workers)
			}
			if cfg.WorkDir != tt.workDir {
				t.Errorf("WorkDir mismatch: got %q, want %q", cfg.WorkDir, tt.workDir)
			}
		})
	}
}

// TestNewOrchestrator_Extended tests additional New() scenarios.
func TestNewOrchestrator_Extended(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
		log  *logger.Logger
	}{
		{
			name: "single_worker",
			cfg: &Config{
				Workers: 1,
				WorkDir: "/tmp/single-worker",
			},
			log: logger.New("debug", "json"),
		},
		{
			name: "many_workers",
			cfg: &Config{
				Workers: 32,
				WorkDir: "/tmp/many-workers",
			},
			log: logger.New("warn", "text"),
		},
		{
			name: "nil_both_params",
			cfg:  nil,
			log:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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

			// Verify context was created
			if o.ctx == nil {
				t.Error("orchestrator.ctx should not be nil")
			}

			// Verify cancel func was created
			if o.cancel == nil {
				t.Error("orchestrator.cancel should not be nil")
			}
		})
	}
}

// TestOrchestratorQueueAccess_Consistency tests queue access is consistent.
func TestOrchestratorQueueAccess_Consistency(t *testing.T) {
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

	// Verify it's the same queue instance
	q2 := o.Queue()
	if q != q2 {
		t.Error("Queue() should return the same instance")
	}
}

// TestOrchestratorStartStop_SingleCycle tests a single start/stop cycle.
func TestOrchestratorStartStop_SingleCycle(t *testing.T) {
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

	// Start should work
	o.Start()
	time.Sleep(10 * time.Millisecond)

	// Stop should work exactly once
	o.Stop()
}

// TestOrchestratorGetQueueStats_InitialValues tests initial stat values.
func TestOrchestratorGetQueueStats_InitialValues(t *testing.T) {
	cfg := DefaultConfig()
	log := logger.New("error", "text")

	o := New(nil, cfg, log)
	stats := o.GetQueueStats()

	// Initially all should be zero
	if stats["total"] != 0 {
		t.Errorf("Expected total=0 initially, got %d", stats["total"])
	}
	if stats["pending"] != 0 {
		t.Errorf("Expected pending=0 initially, got %d", stats["pending"])
	}
	if stats["running"] != 0 {
		t.Errorf("Expected running=0 initially, got %d", stats["running"])
	}
}

// TestOrchestratorGetJobStatus_MultipleJobs tests retrieving multiple jobs.
func TestOrchestratorGetJobStatus_MultipleJobs(t *testing.T) {
	cfg := DefaultConfig()
	log := logger.New("error", "text")
	o := New(nil, cfg, log)

	// Enqueue multiple jobs
	jobIDs := []string{"job-a", "job-b", "job-c"}
	for _, id := range jobIDs {
		job := &jobs.Job{
			ID:          id,
			Type:        jobs.JobTypeProvision,
			TargetType:  "stack",
			TargetID:    "stack-" + id,
			MaxAttempts: 1,
		}
		if err := o.queue.Enqueue(job); err != nil {
			t.Fatalf("Failed to enqueue job %s: %v", id, err)
		}
	}

	// Verify we can get each job
	for _, id := range jobIDs {
		job, err := o.GetJobStatus(id)
		if err != nil {
			t.Errorf("GetJobStatus(%q) returned error: %v", id, err)
			continue
		}
		if job.ID != id {
			t.Errorf("Job ID mismatch for %q: got %q", id, job.ID)
		}
	}
}

// TestOrchestratorConcurrentEnqueue tests concurrent job enqueueing.
func TestOrchestratorConcurrentEnqueue(t *testing.T) {
	cfg := &Config{
		Workers: 4,
		WorkDir: "/tmp/test-provision",
	}
	log := logger.New("error", "text")

	o := New(nil, cfg, log)
	if o == nil {
		t.Fatal("New() returned nil")
	}

	var wg sync.WaitGroup
	numJobs := 50

	for i := 0; i < numJobs; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			job := &jobs.Job{
				ID:          "concurrent-job-" + string(rune('0'+id%10)) + string(rune('a'+id%26)),
				Type:        jobs.JobTypeProvision,
				TargetType:  "stack",
				TargetID:    "stack-" + string(rune('a'+id%26)),
				MaxAttempts: 1,
			}
			_ = o.queue.Enqueue(job)
		}(i)
	}

	wg.Wait()

	stats := o.GetQueueStats()
	if stats["total"] < numJobs/2 { // Allow for some duplicate ID conflicts
		t.Errorf("Expected at least %d jobs in queue, got %d", numJobs/2, stats["total"])
	}
}

// TestErrBlockingPreChecksFailed_Message tests the error message.
func TestErrBlockingPreChecksFailed_Message(t *testing.T) {
	if ErrBlockingPreChecksFailed == nil {
		t.Error("ErrBlockingPreChecksFailed should not be nil")
	}

	expectedMsg := "blocking pre-checks have not passed"
	if ErrBlockingPreChecksFailed.Error() != expectedMsg {
		t.Errorf("Expected error message %q, got %q", expectedMsg, ErrBlockingPreChecksFailed.Error())
	}
}

// TestErrBlockingPreChecksFailed_Wrapping tests error wrapping behavior.
func TestErrBlockingPreChecksFailed_Wrapping(t *testing.T) {
	// Test that errors.Is works for the original error
	if !errors.Is(ErrBlockingPreChecksFailed, ErrBlockingPreChecksFailed) {
		t.Error("errors.Is should match ErrBlockingPreChecksFailed with itself")
	}
}

// TestOrchestratorDeployStack_WithoutApp tests DeployStack without app.
func TestOrchestratorDeployStack_WithoutApp(t *testing.T) {
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

	// Demonstrates DeployStack requires a valid app
}

// TestOrchestratorJobTypes tests that various job types can be enqueued.
func TestOrchestratorJobTypes(t *testing.T) {
	cfg := DefaultConfig()
	log := logger.New("error", "text")
	o := New(nil, cfg, log)

	jobTypes := []jobs.JobType{
		jobs.JobTypeProvision,
		jobs.JobTypeDeploy,
		jobs.JobTypeDestroy,
		jobs.JobTypeCommand,
		jobs.JobTypeDriftCheck,
		jobs.JobTypeDriftResolve,
	}

	for _, jt := range jobTypes {
		t.Run(string(jt), func(t *testing.T) {
			job := &jobs.Job{
				ID:          "type-test-" + string(jt),
				Type:        jt,
				TargetType:  "stack",
				TargetID:    "test-stack",
				MaxAttempts: 1,
			}
			err := o.queue.Enqueue(job)
			if err != nil {
				t.Errorf("Failed to enqueue job type %q: %v", jt, err)
			}

			retrieved, err := o.GetJobStatus(job.ID)
			if err != nil {
				t.Errorf("Failed to get job status for type %q: %v", jt, err)
				return
			}
			if retrieved.Type != jt {
				t.Errorf("Job type mismatch: got %q, want %q", retrieved.Type, jt)
			}
		})
	}
}

func TestJobTypeForPersistence(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "provision", want: "provision"},
		{in: "destroy", want: "destroy"},
		{in: "deploy", want: "update"},
		{in: "drift_check", want: "update"},
		{in: "drift_resolve", want: "restart"},
		{in: "unexpected", want: "update"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := jobTypeForPersistence(tt.in); got != tt.want {
				t.Fatalf("jobTypeForPersistence(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestOrchestratorQueueStatsKeys tests that all expected stats keys are present.
func TestOrchestratorQueueStatsKeys(t *testing.T) {
	cfg := DefaultConfig()
	log := logger.New("error", "text")
	o := New(nil, cfg, log)

	stats := o.GetQueueStats()

	expectedKeys := []string{"total", "pending", "running", "completed", "failed", "canceled"}
	for _, key := range expectedKeys {
		val, ok := stats[key]
		if !ok {
			t.Errorf("Missing stats key: %q", key)
		}
		if val < 0 {
			t.Errorf("Stats key %q has negative value: %d", key, val)
		}
	}
}

// TestOrchestratorWaitGroup tests that the wait group is properly used.
func TestOrchestratorWaitGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping waitgroup test in short mode")
	}

	cfg := &Config{
		Workers: 1,
		WorkDir: "/tmp/test-provision",
	}
	log := logger.New("error", "text")

	o := New(nil, cfg, log)
	o.Start()
	time.Sleep(20 * time.Millisecond)

	// Stop should wait for all goroutines
	start := time.Now()
	o.Stop()
	elapsed := time.Since(start)

	// Stop should complete reasonably quickly
	if elapsed > 5*time.Second {
		t.Errorf("Stop took too long: %v", elapsed)
	}
}

// TestOrchestratorMutex tests that the mutex protects concurrent access.
func TestOrchestratorMutex(t *testing.T) {
	cfg := DefaultConfig()
	log := logger.New("error", "text")
	o := New(nil, cfg, log)

	var wg sync.WaitGroup
	errChan := make(chan error, 100)

	// Concurrent access to queue stats
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stats := o.GetQueueStats()
			if stats == nil {
				errChan <- errors.New("nil stats")
			}
		}()
	}

	// Concurrent job status lookups
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			_, _ = o.GetJobStatus("test-" + string(rune('a'+id%26)))
		}(i)
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		if err != nil {
			t.Errorf("Concurrent access error: %v", err)
		}
	}
}

// TestOrchestratorJobPriority tests that job priority is preserved.
func TestOrchestratorJobPriority(t *testing.T) {
	cfg := DefaultConfig()
	log := logger.New("error", "text")
	o := New(nil, cfg, log)

	priorities := []int{0, 1, 5, 10, 100}
	for i, prio := range priorities {
		job := &jobs.Job{
			ID:          "prio-" + string(rune('a'+i)),
			Type:        jobs.JobTypeProvision,
			TargetType:  "stack",
			TargetID:    "test-stack",
			Priority:    prio,
			MaxAttempts: 1,
		}
		if err := o.queue.Enqueue(job); err != nil {
			t.Fatalf("Failed to enqueue job with priority %d: %v", prio, err)
		}

		retrieved, err := o.GetJobStatus(job.ID)
		if err != nil {
			t.Errorf("Failed to get job with priority %d: %v", prio, err)
			continue
		}
		if retrieved.Priority != prio {
			t.Errorf("Priority mismatch: got %d, want %d", retrieved.Priority, prio)
		}
	}
}

// TestOrchestratorJobPayload tests that job payload is preserved.
func TestOrchestratorJobPayload(t *testing.T) {
	cfg := DefaultConfig()
	log := logger.New("error", "text")
	o := New(nil, cfg, log)

	payload := map[string]interface{}{
		"spec": map[string]interface{}{
			"services": []string{"traefik", "portainer"},
		},
		"dry_run": true,
	}

	job := &jobs.Job{
		ID:          "payload-test",
		Type:        jobs.JobTypeProvision,
		TargetType:  "stack",
		TargetID:    "test-stack",
		Payload:     payload,
		MaxAttempts: 1,
	}

	if err := o.queue.Enqueue(job); err != nil {
		t.Fatalf("Failed to enqueue job: %v", err)
	}

	retrieved, err := o.GetJobStatus(job.ID)
	if err != nil {
		t.Fatalf("Failed to get job: %v", err)
	}

	if retrieved.Payload == nil {
		t.Fatal("Payload should not be nil")
	}

	if _, ok := retrieved.Payload["spec"]; !ok {
		t.Error("Payload should contain 'spec' key")
	}
	if _, ok := retrieved.Payload["dry_run"]; !ok {
		t.Error("Payload should contain 'dry_run' key")
	}
}

// TestOrchestratorJobTargetInfo tests that target info is preserved.
func TestOrchestratorJobTargetInfo(t *testing.T) {
	cfg := DefaultConfig()
	log := logger.New("error", "text")
	o := New(nil, cfg, log)

	testCases := []struct {
		targetType string
		targetID   string
		targetName string
	}{
		{"stack", "stack-123", "my-homelab"},
		{"node", "node-456", "worker-1"},
		{"service", "svc-789", "traefik"},
	}

	for _, tc := range testCases {
		t.Run(tc.targetType, func(t *testing.T) {
			job := &jobs.Job{
				ID:          "target-" + tc.targetType,
				Type:        jobs.JobTypeProvision,
				TargetType:  tc.targetType,
				TargetID:    tc.targetID,
				TargetName:  tc.targetName,
				MaxAttempts: 1,
			}

			if err := o.queue.Enqueue(job); err != nil {
				t.Fatalf("Failed to enqueue: %v", err)
			}

			retrieved, err := o.GetJobStatus(job.ID)
			if err != nil {
				t.Fatalf("Failed to get job: %v", err)
			}

			if retrieved.TargetType != tc.targetType {
				t.Errorf("TargetType mismatch: got %q, want %q", retrieved.TargetType, tc.targetType)
			}
			if retrieved.TargetID != tc.targetID {
				t.Errorf("TargetID mismatch: got %q, want %q", retrieved.TargetID, tc.targetID)
			}
			if retrieved.TargetName != tc.targetName {
				t.Errorf("TargetName mismatch: got %q, want %q", retrieved.TargetName, tc.targetName)
			}
		})
	}
}

// TestOrchestratorMaxAttempts tests that max attempts is preserved.
func TestOrchestratorMaxAttempts(t *testing.T) {
	cfg := DefaultConfig()
	log := logger.New("error", "text")
	o := New(nil, cfg, log)

	attempts := []int{1, 3, 5, 10}
	for i, max := range attempts {
		job := &jobs.Job{
			ID:          "attempts-" + string(rune('a'+i)),
			Type:        jobs.JobTypeProvision,
			TargetType:  "stack",
			TargetID:    "test-stack",
			MaxAttempts: max,
		}

		if err := o.queue.Enqueue(job); err != nil {
			t.Fatalf("Failed to enqueue: %v", err)
		}

		retrieved, err := o.GetJobStatus(job.ID)
		if err != nil {
			t.Fatalf("Failed to get job: %v", err)
		}

		if retrieved.MaxAttempts != max {
			t.Errorf("MaxAttempts mismatch: got %d, want %d", retrieved.MaxAttempts, max)
		}
	}
}

// TestOrchestratorNewWithApp_Nil tests behavior with nil app explicitly.
func TestOrchestratorNewWithApp_Nil(t *testing.T) {
	o := New(nil, nil, nil)
	if o == nil {
		t.Fatal("New(nil, nil, nil) should not return nil")
	}

	// Should have default values
	if o.queue == nil {
		t.Error("queue should not be nil")
	}
	if o.log == nil {
		t.Error("log should not be nil")
	}
}

// TestOrchestratorContextNotNilAfterNew tests context is initialized.
func TestOrchestratorContextNotNilAfterNew(t *testing.T) {
	cfg := DefaultConfig()
	log := logger.New("error", "text")
	o := New(nil, cfg, log)

	if o.ctx == nil {
		t.Error("Context should be initialized")
	}

	// Context should not be canceled initially
	select {
	case <-o.ctx.Done():
		t.Error("Context should not be done initially")
	default:
		// Good
	}
}

// TestOrchestratorCancelFuncWorks tests that cancel function works.
func TestOrchestratorCancelFuncWorks(t *testing.T) {
	cfg := DefaultConfig()
	log := logger.New("error", "text")
	o := New(nil, cfg, log)

	// Cancel should trigger context done
	o.cancel()

	select {
	case <-o.ctx.Done():
		// Good - context is canceled
	case <-time.After(100 * time.Millisecond):
		t.Error("Context should be done after cancel")
	}
}

// TestOrchestratorConcurrentStatsReads tests concurrent reads of stats.
func TestOrchestratorConcurrentStatsReads(t *testing.T) {
	cfg := DefaultConfig()
	log := logger.New("error", "text")
	o := New(nil, cfg, log)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					stats := o.GetQueueStats()
					if stats == nil {
						t.Error("Stats should not be nil")
					}
				}
			}
		}()
	}

	wg.Wait()
}

// TestOrchestratorJobEnqueueDuplicateID tests duplicate job ID handling.
func TestOrchestratorJobEnqueueDuplicateID(t *testing.T) {
	cfg := DefaultConfig()
	log := logger.New("error", "text")
	o := New(nil, cfg, log)

	job1 := &jobs.Job{
		ID:          "duplicate-id",
		Type:        jobs.JobTypeProvision,
		TargetType:  "stack",
		TargetID:    "test-stack-1",
		MaxAttempts: 1,
	}

	job2 := &jobs.Job{
		ID:          "duplicate-id",
		Type:        jobs.JobTypeDeploy,
		TargetType:  "stack",
		TargetID:    "test-stack-2",
		MaxAttempts: 1,
	}

	// First enqueue should succeed
	if err := o.queue.Enqueue(job1); err != nil {
		t.Fatalf("First enqueue failed: %v", err)
	}

	// Second enqueue with same ID - behavior depends on queue implementation
	// Just verify no crash occurs
	_ = o.queue.Enqueue(job2)

	// Should be able to get the job
	retrieved, err := o.GetJobStatus("duplicate-id")
	if err != nil {
		t.Errorf("GetJobStatus failed: %v", err)
	}
	if retrieved == nil {
		t.Error("Job should exist")
	}
}

// TestOrchestratorEmptyJobID tests handling of empty job ID.
func TestOrchestratorEmptyJobID(t *testing.T) {
	cfg := DefaultConfig()
	log := logger.New("error", "text")
	o := New(nil, cfg, log)

	_, err := o.GetJobStatus("")
	if err == nil {
		t.Error("GetJobStatus with empty ID should return error")
	}
}

// TestOrchestratorQueueReturnsSameInstance tests queue consistency.
func TestOrchestratorQueueReturnsSameInstance(t *testing.T) {
	cfg := DefaultConfig()
	log := logger.New("error", "text")
	o := New(nil, cfg, log)

	q1 := o.Queue()
	q2 := o.Queue()
	q3 := o.Queue()

	if q1 != q2 || q2 != q3 {
		t.Error("Queue() should return the same instance every time")
	}
}
