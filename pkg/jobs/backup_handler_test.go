package jobs

import (
	"context"
	"errors"
	"testing"
)

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
	commander := &recordingCommander{}
	handler := BackupHandler(&ProvisionConfig{
		StackKitCommander: commander,
		BackupAgentResolver: func(context.Context, string, string) (string, error) {
			return "worker-7", nil
		},
	})

	job := &Job{Type: JobTypeBackup, Payload: map[string]interface{}{"stack_id": "stack-a"}}
	if err := handler(context.Background(), job, nil); err == nil {
		t.Fatal("a job without tenant identity must fail before dispatch")
	}
	if len(commander.commands) != 0 {
		t.Fatalf("no node command may be sent without exact identity: %+v", commander.commands)
	}
}
