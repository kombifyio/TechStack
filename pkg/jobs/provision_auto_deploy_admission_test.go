package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

func autoDeployAdmissionTestJob(id string) *Job {
	return &Job{
		ID: id, Type: JobTypeProvision, TargetID: "stack-1",
		Payload: map[string]interface{}{"auto_deploy": true, "tenant_id": "tenant-1", "owner_id": "owner-1"},
		Result:  map[string]interface{}{"lease_id": "lease-1"},
	}
}

func TestProvisionMaybeAutoDeployFailsClosedBeforePayloadMutationWithoutAdmission(t *testing.T) {
	job := autoDeployAdmissionTestJob("auto-deploy-no-admission")
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	handled, err := provisionMaybeAutoDeploy(context.Background(), &ProvisionConfig{}, job, queue)
	if !handled {
		t.Fatal("auto-deploy request was not handled")
	}
	if !errors.Is(err, ErrAutoDeployAdmissionUnavailable) {
		t.Fatalf("error = %v, want ErrAutoDeployAdmissionUnavailable", err)
	}
	snapshot := job.Snapshot()
	if snapshot.Type != JobTypeProvision {
		t.Fatalf("job type = %q, want provision before admission", snapshot.Type)
	}
	if _, exists := snapshot.Payload["apply"]; exists {
		t.Fatal("deploy payload was prepared before canonical Guard admission")
	}
}

func TestProvisionMaybeAutoDeployGuardWaitTimesOutFailClosed(t *testing.T) {
	starts := map[string]func(*Job, string){
		"durable result": func(job *Job, startedAt string) { job.Result[autoDeployGuardWaitStartedAtField] = startedAt },
		"legacy payload": func(job *Job, startedAt string) { job.Payload[autoDeployGuardWaitStartedAtField] = startedAt },
	}

	for name, seedStart := range starts {
		t.Run(name, func(t *testing.T) {
			job := autoDeployAdmissionTestJob("auto-deploy-guard-timeout")
			seedStart(job, time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano))
			queue := &Queue{jobs: map[string]*Job{job.ID: job}}
			cfg := &ProvisionConfig{
				ManagedRuntimeTargetWaitTimeout: time.Millisecond,
				AutoDeployAdmission: func(context.Context, AutoDeployAdmissionRequest) error {
					return errors.New("still unavailable")
				},
			}

			handled, err := provisionMaybeAutoDeploy(context.Background(), cfg, job, queue)
			if !handled || !errors.Is(err, ErrAutoDeployAdmissionTimeout) {
				t.Fatalf("handled=%v error=%v, want fail-closed timeout", handled, err)
			}
			snapshot := job.Snapshot()
			if snapshot.Type != JobTypeProvision {
				t.Fatalf("job type = %q, want provision after timeout", snapshot.Type)
			}
			if _, exists := snapshot.Payload["apply"]; exists {
				t.Fatal("deploy payload was prepared after admission timeout")
			}
		})
	}
}
