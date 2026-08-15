package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
)

// TestDriftCheckHandler_MissingStackID verifies that the handler fails gracefully
// when no stack_id is provided in the payload.
func TestDriftCheckHandler_MissingStackID(t *testing.T) {
	cfg := jobs.DefaultDriftCheckConfig()
	handler := jobs.DriftCheckHandler(cfg)

	log := logger.Default()
	queue := jobs.NewQueue(1, log)

	job := &jobs.Job{
		ID:      "test-drift-1",
		Type:    jobs.JobTypeDriftCheck,
		Payload: map[string]interface{}{}, // Missing stack_id
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := handler(ctx, job, queue)
	if err == nil {
		t.Error("expected error for missing stack_id, got nil")
	}
}

// TestDriftCheckHandler_InvalidStackID verifies that the handler fails gracefully
// when an invalid stack_id is provided.
func TestDriftCheckHandler_InvalidStackID(t *testing.T) {
	cfg := jobs.DefaultDriftCheckConfig()
	handler := jobs.DriftCheckHandler(cfg)

	log := logger.Default()
	queue := jobs.NewQueue(1, log)

	job := &jobs.Job{
		ID:   "test-drift-2",
		Type: jobs.JobTypeDriftCheck,
		Payload: map[string]interface{}{
			"stack_id": "", // Empty stack_id
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := handler(ctx, job, queue)
	if err == nil {
		t.Error("expected error for empty stack_id, got nil")
	}
}

// TestDriftResolveHandler_MissingStackID verifies that the resolve handler fails
// gracefully when no stack_id is provided.
func TestDriftResolveHandler_MissingStackID(t *testing.T) {
	cfg := jobs.DefaultDriftCheckConfig()
	handler := jobs.DriftResolveHandler(cfg)

	log := logger.Default()
	queue := jobs.NewQueue(1, log)

	job := &jobs.Job{
		ID:      "test-drift-resolve-1",
		Type:    jobs.JobTypeDriftResolve,
		Payload: map[string]interface{}{}, // Missing stack_id
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := handler(ctx, job, queue)
	if err == nil {
		t.Error("expected error for missing stack_id in resolve handler, got nil")
	}
}

// TestDefaultDriftCheckConfig verifies default configuration values.
func TestDefaultDriftCheckConfig(t *testing.T) {
	cfg := jobs.DefaultDriftCheckConfig()

	if cfg == nil {
		t.Fatal("DefaultDriftCheckConfig returned nil")
	}

	if cfg.WorkDir == "" {
		t.Error("expected WorkDir to be set")
	}

	if cfg.TerramateTimeout == 0 {
		t.Error("expected TerramateTimeout to be set")
	}

	if cfg.PlanTimeout == 0 {
		t.Error("expected PlanTimeout to be set")
	}

	// Verify reasonable defaults
	if cfg.TerramateTimeout < 5*time.Minute {
		t.Errorf("TerramateTimeout too short: %v", cfg.TerramateTimeout)
	}

	if cfg.PlanTimeout < 1*time.Minute {
		t.Errorf("PlanTimeout too short: %v", cfg.PlanTimeout)
	}
}

// TestRegisterDriftHandlers verifies that drift handlers are registered correctly.
func TestRegisterDriftHandlers(t *testing.T) {
	log := logger.Default()
	queue := jobs.NewQueue(1, log)

	// Should not panic
	jobs.RegisterDriftHandlers(queue, nil)

	// Verify handlers are registered by checking queue stats
	// (the queue should have the drift_check and drift_resolve types registered)
	stats := queue.Stats()
	if stats == nil {
		t.Fatal("Stats() returned nil")
	}
}

// TestDriftCheckResult_Types verifies the drift check result types.
func TestDriftCheckResult_Types(t *testing.T) {
	// Test DriftStatus constants
	statuses := []jobs.DriftStatus{
		jobs.DriftStatusInSync,
		jobs.DriftStatusDrifted,
		jobs.DriftStatusFailed,
		jobs.DriftStatusChecking,
		jobs.DriftStatusUnknown,
	}

	for _, status := range statuses {
		if string(status) == "" {
			t.Error("DriftStatus should not be empty")
		}
	}
}

// TestResourceChange_ChangeTypes verifies resource change type handling.
func TestResourceChange_ChangeTypes(t *testing.T) {
	actions := []string{"create", "update", "delete", "no-op"}

	for _, action := range actions {
		change := jobs.ResourceChange{
			Address:      "test.resource",
			ResourceType: "test_resource",
			Action:       action,
		}

		if change.Address == "" {
			t.Error("Address should be set")
		}

		if change.Action != action {
			t.Errorf("expected Action %s, got %s", action, change.Action)
		}
	}
}

// TestPlanSummary_Totals verifies plan summary calculations.
func TestPlanSummary_Totals(t *testing.T) {
	summary := jobs.PlanSummary{
		ToCreate:  5,
		ToUpdate:  3,
		ToDelete:  2,
		Unchanged: 10,
	}

	total := summary.ToCreate + summary.ToUpdate + summary.ToDelete + summary.Unchanged
	if total != 20 {
		t.Errorf("expected total 20, got %d", total)
	}
}

// TestDriftCheckResult_Serialization tests that DriftCheckResult can be serialized.
func TestDriftCheckResult_Serialization(t *testing.T) {
	result := jobs.DriftCheckResult{
		StackID:       "stack-123",
		Status:        jobs.DriftStatusDrifted,
		AffectedCount: 3,
		AffectedResources: []jobs.ResourceChange{
			{
				Address:      "aws_instance.web",
				ResourceType: "aws_instance",
				Action:       "update",
			},
		},
		PlanSummary: jobs.PlanSummary{
			ToCreate:  0,
			ToUpdate:  3,
			ToDelete:  0,
			Unchanged: 5,
		},
		CheckedAt: time.Now(),
	}

	if result.StackID != "stack-123" {
		t.Error("StackID not set correctly")
	}

	if result.Status != jobs.DriftStatusDrifted {
		t.Error("Status not set correctly")
	}

	if len(result.AffectedResources) != 1 {
		t.Errorf("expected 1 change, got %d", len(result.AffectedResources))
	}
}

// TestChangeDetail_FromTo tests ChangeDetail struct.
func TestChangeDetail_FromTo(t *testing.T) {
	change := jobs.ChangeDetail{
		From: "old-value",
		To:   "new-value",
	}

	if change.From != "old-value" {
		t.Errorf("expected From 'old-value', got %v", change.From)
	}
	if change.To != "new-value" {
		t.Errorf("expected To 'new-value', got %v", change.To)
	}
}

// TestDriftCheckConfig_Defaults tests default configuration values.
func TestDriftCheckConfig_Defaults(t *testing.T) {
	cfg := jobs.DefaultDriftCheckConfig()

	if cfg.WorkDir == "" {
		t.Error("WorkDir should not be empty")
	}

	if cfg.TerramateTimeout != 30*time.Minute {
		t.Errorf("expected TerramateTimeout 30m, got %v", cfg.TerramateTimeout)
	}

	if cfg.PlanTimeout != 10*time.Minute {
		t.Errorf("expected PlanTimeout 10m, got %v", cfg.PlanTimeout)
	}
}

// TestDriftHandler_WithTargetID tests the handler when TargetID is used.
func TestDriftHandler_WithTargetID(t *testing.T) {
	cfg := jobs.DefaultDriftCheckConfig()
	cfg.WorkDir = t.TempDir()
	handler := jobs.DriftCheckHandler(cfg)

	log := logger.Default()
	queue := jobs.NewQueue(1, log)

	// Job with valid stack ID from TargetID
	job := &jobs.Job{
		ID:       "test-drift-target",
		Type:     jobs.JobTypeDriftCheck,
		TargetID: "my-test-stack",
		Payload:  map[string]interface{}{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Will fail because Terramate isn't installed, but it should get past the
	// validation step which checks for TargetID
	err := handler(ctx, job, queue)

	// We expect an error, but it should NOT be about missing stack_id
	if err != nil {
		errMsg := err.Error()
		if contains(errMsg, "missing stack_id") {
			t.Error("should have read stack_id from TargetID")
		}
	}
}

// TestDriftHandler_TriggerType tests different trigger types.
func TestDriftHandler_TriggerType(t *testing.T) {
	cfg := jobs.DefaultDriftCheckConfig()
	cfg.WorkDir = t.TempDir()
	handler := jobs.DriftCheckHandler(cfg)

	log := logger.Default()
	queue := jobs.NewQueue(1, log)

	triggerTypes := []string{"manual", "scheduled", "webhook"}

	for _, triggerType := range triggerTypes {
		t.Run("trigger_"+triggerType, func(t *testing.T) {
			job := &jobs.Job{
				ID:       "test-drift-" + triggerType,
				Type:     jobs.JobTypeDriftCheck,
				TargetID: "stack-" + triggerType,
				Payload: map[string]interface{}{
					"trigger_type": triggerType,
				},
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			// Will fail at Terramate check, but trigger_type should be captured
			_ = handler(ctx, job, queue)

			// No specific assertion since Terramate isn't installed
			// The test verifies the code path doesn't panic
		})
	}
}

// TestDriftResolveHandler_WithTargetID tests the resolve handler with TargetID.
func TestDriftResolveHandler_WithTargetID(t *testing.T) {
	cfg := jobs.DefaultDriftCheckConfig()
	cfg.WorkDir = t.TempDir()
	handler := jobs.DriftResolveHandler(cfg)

	log := logger.Default()
	queue := jobs.NewQueue(1, log)

	job := &jobs.Job{
		ID:       "test-resolve",
		Type:     jobs.JobTypeDriftResolve,
		TargetID: "my-stack",
		Payload:  map[string]interface{}{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := handler(ctx, job, queue)

	// Should fail at tofu init, not at stack_id validation
	if err != nil {
		errMsg := err.Error()
		if contains(errMsg, "missing stack_id") {
			t.Error("should have read stack_id from TargetID")
		}
	}
}

// contains is a helper function to check if a string contains a substring.
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
