package jobs

import (
	"context"
	"errors"
	"testing"
)

type recordingBackupRunner struct {
	calls []RuntimeActionRequest
	err   error
}

func (r *recordingBackupRunner) Run(_ context.Context, req RuntimeActionRequest) error {
	r.calls = append(r.calls, req)
	return r.err
}

// TestAdmitBackupFailsClosed covers every way the gate can be unable to answer.
// Each of them must deny. An unconfigured, erroring or unidentifiable gate that
// returned approval would turn a fail-closed contract into a fail-open one at
// exactly the moment it matters.
func TestAdmitBackupFailsClosed(t *testing.T) {
	ctx := context.Background()
	valid := BackupAdmissionRequest{TenantID: "tenant-a", StackID: "stack-a", UserID: "user-a"}

	// No authority wired at all.
	decision, err := AdmitBackup(ctx, nil, valid)
	if !decision.Denied || !errors.Is(err, ErrBackupAdmissionUnavailable) {
		t.Fatalf("missing authority must deny: denied=%v err=%v", decision.Denied, err)
	}

	// The authority is reachable but fails.
	failing := BackupAdmission(func(context.Context, BackupAdmissionRequest) (BackupAdmissionDecision, error) {
		return BackupAdmissionDecision{}, errors.New("edge down")
	})
	if decision, err := AdmitBackup(ctx, failing, valid); !decision.Denied || err == nil {
		t.Fatalf("a failing authority must deny: denied=%v err=%v", decision.Denied, err)
	}

	// Identity is incomplete, so the subject cannot be gated.
	allowAll := BackupAdmission(func(context.Context, BackupAdmissionRequest) (BackupAdmissionDecision, error) {
		t.Fatal("the authority must not be consulted without complete identity")
		return BackupAdmissionDecision{}, nil
	})
	for _, incomplete := range []BackupAdmissionRequest{
		{StackID: "stack-a", UserID: "user-a"},
		{TenantID: "tenant-a", UserID: "user-a"},
		{TenantID: "tenant-a", StackID: "stack-a"},
	} {
		if decision, err := AdmitBackup(ctx, allowAll, incomplete); !decision.Denied || err == nil {
			t.Fatalf("incomplete identity must deny: %+v", incomplete)
		}
	}
}

func TestAdmitBackupCarriesTheDenialEnvelope(t *testing.T) {
	over := BackupAdmission(func(context.Context, BackupAdmissionRequest) (BackupAdmissionDecision, error) {
		return BackupAdmissionDecision{
			Denied:     true,
			QuotaBytes: 50 << 30,
			UsedBytes:  60 << 30,
			Details:    map[string]any{"reason_code": "storage_quota_exceeded"},
		}, nil
	})
	decision, err := AdmitBackup(context.Background(), over, BackupAdmissionRequest{
		TenantID: "tenant-a", StackID: "stack-a", UserID: "user-a",
	})
	if err != nil {
		t.Fatalf("a quota denial is a decision, not a transport error: %v", err)
	}
	if !decision.Denied || decision.Details["reason_code"] != "storage_quota_exceeded" {
		t.Fatalf("denial envelope lost: %+v", decision)
	}
}

// TestBackupHandlerRefusesAnUnconfiguredRunner keeps an unwired runtime from
// reporting a successful backup that never reached a node.
func TestBackupHandlerRefusesAnUnconfiguredRunner(t *testing.T) {
	handler := BackupHandler(&ProvisionConfig{})
	job := &Job{Type: JobTypeBackup, TargetID: "stack-a", Payload: map[string]interface{}{"tenant_id": "tenant-a"}}
	if err := handler(context.Background(), job, nil); err == nil {
		t.Fatal("an unconfigured backup runner must fail, not silently succeed")
	}
}

func TestBackupHandlerRequiresExactIdentity(t *testing.T) {
	runner := &recordingBackupRunner{}
	handler := BackupHandler(&ProvisionConfig{RuntimeActions: RuntimeActions{BackupRunner: runner}})

	job := &Job{Type: JobTypeBackup, Payload: map[string]interface{}{"stack_id": "stack-a"}}
	if err := handler(context.Background(), job, nil); err == nil {
		t.Fatal("a job without tenant identity must fail before dispatch")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("no node call may happen without exact identity: %+v", runner.calls)
	}
}

func TestBackupHandlerDispatchesWithResolvedIdentity(t *testing.T) {
	runner := &recordingBackupRunner{}
	handler := BackupHandler(&ProvisionConfig{RuntimeActions: RuntimeActions{BackupRunner: runner}})

	job := &Job{
		Type: JobTypeBackup, TargetID: "stack-a", TargetName: "photos",
		Payload: map[string]interface{}{"tenant_id": "tenant-a", "owner_id": "owner-a"},
	}
	if err := handler(context.Background(), job, nil); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected exactly one backup dispatch, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if call.TenantID != "tenant-a" || call.StackID != "stack-a" || call.StackName != "photos" || call.OwnerID != "owner-a" {
		t.Fatalf("dispatch identity = %+v", call)
	}
}
