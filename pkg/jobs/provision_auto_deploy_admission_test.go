package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProvisionMaybeAutoDeployFailsClosedBeforePayloadMutationWithoutAdmission(t *testing.T) {
	job := &Job{
		ID:       "auto-deploy-no-admission",
		Type:     JobTypeProvision,
		TargetID: "stack-1",
		Payload: map[string]interface{}{
			"auto_deploy": true,
			"tenant_id":   "tenant-1",
			"owner_id":    "owner-1",
		},
		Result: map[string]interface{}{"lease_id": "lease-1"},
	}
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

func TestProvisionMaybeAutoDeployWaitsBeforePayloadMutationForGuardEvidence(t *testing.T) {
	job := &Job{
		ID:       "auto-deploy-waiting-guard",
		Type:     JobTypeProvision,
		TargetID: "stack-1",
		Payload: map[string]interface{}{
			"auto_deploy": true,
			"tenant_id":   "tenant-1",
			"owner_id":    "owner-1",
		},
		Result: map[string]interface{}{"lease_id": "lease-1"},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}
	wantCause := errors.New("no fresh canonical Guard runtime")
	cfg := &ProvisionConfig{
		ManagedRuntimeTargetWaitTimeout:  time.Second,
		ManagedRuntimeTargetPollInterval: 10 * time.Millisecond,
		AutoDeployAdmission: func(_ context.Context, req AutoDeployAdmissionRequest) error {
			if req.StackID != "stack-1" || req.TenantID != "tenant-1" || req.OwnerID != "owner-1" || req.LeaseID != "lease-1" {
				t.Fatalf("admission request = %+v", req)
			}
			return wantCause
		},
	}

	handled, err := provisionMaybeAutoDeploy(context.Background(), cfg, job, queue)
	if !handled {
		t.Fatal("auto-deploy request was not handled")
	}
	var waitErr *JobWaitError
	if !errors.As(err, &waitErr) {
		t.Fatalf("error = %T %v, want JobWaitError", err, err)
	}
	if waitErr.Reason != WaitReasonCanonicalGuardEvidence || !errors.Is(err, wantCause) {
		t.Fatalf("wait error = %+v, want canonical Guard cause", waitErr)
	}
	snapshot := job.Snapshot()
	if snapshot.Type != JobTypeProvision {
		t.Fatalf("job type = %q, want provision while waiting", snapshot.Type)
	}
	if _, exists := snapshot.Payload["apply"]; exists {
		t.Fatal("deploy payload was prepared while canonical Guard admission was pending")
	}
	if snapshot.Result[autoDeployGuardWaitStartedAtField] == nil {
		t.Fatal("bounded Guard wait start was not persisted")
	}
	durableResult := cloneJobResult(snapshot.Result)
	rehydrated := &Job{Payload: cloneJobMap(snapshot.Payload), Result: durableResult}
	if rehydrated.Snapshot().Result[autoDeployGuardWaitStartedAtField] == nil {
		t.Fatal("bounded Guard wait start was lost across a durable result roundtrip")
	}
}

func TestProvisionMaybeAutoDeployGuardWaitTimesOutFailClosed(t *testing.T) {
	job := &Job{
		ID:       "auto-deploy-guard-timeout",
		Type:     JobTypeProvision,
		TargetID: "stack-1",
		Payload: map[string]interface{}{
			"auto_deploy":                     true,
			"tenant_id":                       "tenant-1",
			"owner_id":                        "owner-1",
			autoDeployGuardWaitStartedAtField: time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano),
		},
		Result: map[string]interface{}{"lease_id": "lease-1"},
	}
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
}

func TestProvisionMaybeAutoDeployGuardWaitReadsLegacyPayloadStart(t *testing.T) {
	job := &Job{
		ID:       "auto-deploy-guard-legacy-timeout",
		Type:     JobTypeProvision,
		TargetID: "stack-1",
		Payload: map[string]interface{}{
			"auto_deploy":                     true,
			"tenant_id":                       "tenant-1",
			"owner_id":                        "owner-1",
			autoDeployGuardWaitStartedAtField: time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano),
		},
		Result: map[string]interface{}{"lease_id": "lease-1"},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}
	cfg := &ProvisionConfig{
		ManagedRuntimeTargetWaitTimeout: time.Millisecond,
		AutoDeployAdmission: func(context.Context, AutoDeployAdmissionRequest) error {
			return errors.New("still unavailable")
		},
	}

	handled, err := provisionMaybeAutoDeploy(context.Background(), cfg, job, queue)
	if !handled || !errors.Is(err, ErrAutoDeployAdmissionTimeout) {
		t.Fatalf("handled=%v error=%v, want legacy payload start to retain fail-closed timeout", handled, err)
	}
}
