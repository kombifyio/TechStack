package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
)

// Both drift handlers require TargetID at validation. Once present, their
// observable job step must prove they crossed that boundary without depending
// on StackKits CLI error wording.
func TestDriftHandlersValidateTargetAtJobBoundary(t *testing.T) {
	tests := []struct {
		name, targetID, wantStep string
		jobType                  jobs.JobType
		resolve, wantError       bool
	}{
		{"check rejects missing target", "", jobs.StepDriftValidate, jobs.JobTypeDriftCheck, false, true},
		{"resolve rejects missing target", "", jobs.StepDriftValidate, jobs.JobTypeDriftResolve, true, true},
		{"check accepts TargetID", "my-test-stack", jobs.StepDriftNotify, jobs.JobTypeDriftCheck, false, false},
		{"resolve accepts TargetID", "my-stack", jobs.StepDriftCheckStacks, jobs.JobTypeDriftResolve, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := jobs.DefaultDriftCheckConfig()
			cfg.WorkDir = t.TempDir()
			handler := jobs.DriftCheckHandler(cfg)
			if tt.resolve {
				handler = jobs.DriftResolveHandler(cfg)
			}
			job := &jobs.Job{ID: tt.name, Type: tt.jobType, TargetID: tt.targetID, Payload: map[string]interface{}{}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			err := handler(ctx, job, jobs.NewQueue(1, logger.Default()))
			if tt.wantError && err == nil {
				t.Fatal("handler accepted a job without TargetID")
			}
			if job.Step != tt.wantStep {
				t.Fatalf("job step = %q, want %q", job.Step, tt.wantStep)
			}
		})
	}
}
