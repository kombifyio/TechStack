package providercontrol

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

func TestCoordinatorPersistsNativeAuthorityAndExecutionFences(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("ionos-v1", &queueExecutor{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry,
		Profiles: staticProfileResolver{profile: testProfile("ionos-v1")},
		Ledger:   newMemoryLedger(),
		Now:      func() time.Time { return contractNow },
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	record, created, err := coordinator.Start(t.Context(), planRequest())
	if err != nil || !created {
		t.Fatalf("Start created=%v error=%v", created, err)
	}
	if record.ExecutionAuthority != ExecutionAuthorityTechstackProviderControl {
		t.Fatalf("authority = %q", record.ExecutionAuthority)
	}
	command := record.Command
	if command.LeaseRevision != 7 || command.RuntimeServerID != "server-1" ||
		command.ResourceGenerationID != testResourceGenerationID || command.ProviderID != "ionos" ||
		command.CapabilitySnapshotHash == "" || command.ExecutionProfileHash == "" {
		t.Fatalf("command fences are incomplete: %+v", command)
	}
	if record.Head.LeaseRevision != command.LeaseRevision ||
		record.Head.RuntimeServerID != command.RuntimeServerID ||
		record.Head.ResourceGenerationID != command.ResourceGenerationID ||
		record.Head.ProviderID != command.ProviderID ||
		record.Head.CapabilitySnapshotHash != command.CapabilitySnapshotHash {
		t.Fatalf("initial receipt lost command fences: %+v", record.Head)
	}
}

func TestCoordinatorNeverExecutesForeignAuthority(t *testing.T) {
	registry := NewRegistry()
	executor := &queueExecutor{}
	if err := registry.Register("ionos-v1", executor); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ledger := newMemoryLedger()
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry,
		Profiles: staticProfileResolver{profile: testProfile("ionos-v1")},
		Ledger:   ledger,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	record, _, err := coordinator.Start(t.Context(), planRequest())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ledger.mu.Lock()
	record.ExecutionAuthority = ExecutionAuthority("legacy_simulate")
	ledger.records[record.Command.OperationID] = record
	ledger.mu.Unlock()
	if _, _, err := coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID); !errors.Is(err, ErrExecutionAuthority) {
		t.Fatalf("Advance error = %v, want ErrExecutionAuthority", err)
	}
	if executor.calls != 0 {
		t.Fatalf("foreign authority invoked adapter %d times", executor.calls)
	}
}

func TestCoordinatorRejectsGenerationReplay(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("ionos-v1", &queueExecutor{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry,
		Profiles: staticProfileResolver{profile: testProfile("ionos-v1")},
		Ledger:   newMemoryLedger(),
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	req := planRequest()
	if _, _, err := coordinator.Start(t.Context(), req); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	req.ResourceGenerationID = "22222222-2222-4222-8222-222222222222"
	if _, _, err := coordinator.Start(t.Context(), req); !errors.Is(err, providerexecutor.ErrIdempotencyConflict) {
		t.Fatalf("generation replay error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestCoordinatorRejectsNonJSONSafeRevisions(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("ionos-v1", &queueExecutor{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*StartRequest)
	}{
		{name: "lease revision", mutate: func(req *StartRequest) { req.LeaseRevision = providerexecutor.MaxJSONSafeInteger + 1 }},
		{name: "ledger revision", mutate: func(req *StartRequest) { req.LedgerRevision = providerexecutor.MaxJSONSafeInteger + 1 }},
		{name: "desired spec revision", mutate: func(req *StartRequest) { req.DesiredSpec.Revision = providerexecutor.MaxJSONSafeInteger + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger := newMemoryLedger()
			coordinator, err := newTestCoordinator(CoordinatorConfig{
				Registry: registry,
				Profiles: staticProfileResolver{profile: testProfile("ionos-v1")},
				Ledger:   ledger,
			})
			if err != nil {
				t.Fatalf("NewCoordinator: %v", err)
			}
			req := planRequest()
			test.mutate(&req)
			if _, _, err := coordinator.Start(t.Context(), req); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Start error = %v, want ErrInvalidRequest", err)
			}
			if ledger.beginCalls != 0 {
				t.Fatalf("unsafe revision reached ledger %d times", ledger.beginCalls)
			}
		})
	}
}

func TestCoordinatorRejectsCompositeProviderID(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("ionos-v1", &queueExecutor{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	profile := testProfile("ionos-v1")
	profile.ProviderID = "ionos-managed"
	ledger := newMemoryLedger()
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry,
		Profiles: staticProfileResolver{profile: profile},
		Ledger:   ledger,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	if _, _, err := coordinator.Start(t.Context(), planRequest()); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Start error = %v, want ErrInvalidRequest", err)
	}
	if ledger.beginCalls != 0 {
		t.Fatal("invalid provider profile reached ledger")
	}
}

func TestCoordinatorRejectsAdapterDenialAfterInvocation(t *testing.T) {
	executor := &queueExecutor{results: []providerexecutor.ExecutionResult{{
		Status: providerexecutor.StatusDenied,
		Phase:  providerexecutor.PhaseDenied,
		Reason: &providerexecutor.Reason{Code: "policy.denied"},
	}}}
	registry := NewRegistry()
	if err := registry.Register("ionos-v1", executor); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ledger := newMemoryLedger()
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry: registry,
		Profiles: staticProfileResolver{profile: testProfile("ionos-v1")},
		Ledger:   ledger,
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	record, _, err := coordinator.Start(t.Context(), planRequest())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	record, _, err = coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil {
		t.Fatalf("Advance accepted: %v", err)
	}
	if _, _, advanceErr := coordinator.Advance(t.Context(), record.Command.TenantID, record.Command.OperationID); !errors.Is(advanceErr, ErrInvalidRequest) {
		t.Fatalf("adapter denial error = %v, want ErrInvalidRequest", advanceErr)
	}
	loaded, err := ledger.LoadOperation(t.Context(), record.Command.TenantID, record.Command.OperationID)
	if err != nil || loaded.Head.ReceiptDigest != record.Head.ReceiptDigest {
		t.Fatalf("adapter denial changed head: loaded=%+v error=%v", loaded.Head, err)
	}
}

func TestCoordinatorAcceptsOnlyFreshProviderAbsenceEvidence(t *testing.T) {
	target := providerexecutor.ResourceTarget{
		BindingID: "server", Kind: "compute", NativeRef: "provider-server-1",
		OwnershipHash: digest("owner"), Disposition: providerexecutor.DispositionDelete,
	}
	executor := &freshAbsenceExecutor{}
	registry := NewRegistry()
	if err := registry.Register("ionos-v1", executor); err != nil {
		t.Fatalf("Register: %v", err)
	}
	verifier := &acceptEvidenceVerifier{}
	now := contractNow
	profile := testProfile("ionos-v1")
	profile.ProvisionDispatchMode = ProvisionDispatchProviderCorrelation
	coordinator, err := newTestCoordinator(CoordinatorConfig{
		Registry:         registry,
		Profiles:         staticProfileResolver{profile: profile},
		Ledger:           newMemoryLedger(),
		EvidenceVerifier: verifier,
		Now: func() time.Time {
			now = now.Add(time.Second)
			return now
		},
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	req := StartRequest{
		TenantID: "tenant-1", LeaseID: "lease-1", LeaseRevision: 7,
		RuntimeServerID: "server-1", ResourceGenerationID: testResourceGenerationID,
		Operation: providerexecutor.OperationDecommission, IdempotencyKey: "delete-fresh",
		LedgerRevision: 4, Targets: []providerexecutor.ResourceTarget{target}, RequestedAt: contractNow,
	}
	record, _, err := coordinator.Start(t.Context(), req)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	for range 3 {
		record, _, err = coordinator.Advance(t.Context(), req.TenantID, record.Command.OperationID)
		if err != nil {
			t.Fatalf("intermediate Advance: %v", err)
		}
	}
	if record.Head.Phase != providerexecutor.PhaseAbsencePending {
		t.Fatalf("phase = %q, want absence_pending", record.Head.Phase)
	}
	absencePendingAt := record.Head.PhaseEnteredAt
	record, advanced, err := coordinator.Advance(t.Context(), req.TenantID, record.Command.OperationID)
	if err != nil || !advanced {
		t.Fatalf("absent Advance advanced=%v error=%v", advanced, err)
	}
	if record.Head.Phase != providerexecutor.PhaseAbsent || !record.Head.PhaseEnteredAt.Equal(record.Head.IssuedAt) {
		t.Fatalf("absent receipt = %+v", record.Head)
	}
	collectedAt := record.Head.Resources[0].Evidence[0].CollectedAt
	if collectedAt.Before(absencePendingAt) || verifier.calls == 0 {
		t.Fatalf("absence evidence collected=%s pending=%s verifier_calls=%d", collectedAt, absencePendingAt, verifier.calls)
	}
}

func TestNewRuntimeRequiresEvidenceVerifierForAdmittedAdapters(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()
	registry := NewRegistry()
	if err := registry.Register("ionos-v1", &queueExecutor{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := NewRuntime(RuntimeConfig{Database: db, Registry: registry}); !errors.Is(err, ErrEvidenceVerifier) {
		t.Fatalf("NewRuntime error = %v, want ErrEvidenceVerifier", err)
	}
}

func TestFailClosedProfileResolver(t *testing.T) {
	if _, err := (FailClosedProfileResolver{}).ResolveExecutionProfile(context.Background(), ProfileRequest{}); !errors.Is(err, ErrProfileUnavailable) {
		t.Fatalf("ResolveExecutionProfile error = %v, want ErrProfileUnavailable", err)
	}
}

type acceptEvidenceVerifier struct{ calls int }

func (v *acceptEvidenceVerifier) VerifyEvidence(
	ctx context.Context,
	_ providerexecutor.Command,
	_ providerexecutor.ResourceTarget,
	_ providerexecutor.Evidence,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	v.calls++
	return nil
}

type freshAbsenceExecutor struct{ calls int }

func (*freshAbsenceExecutor) CrashRecoveryCapability() CrashRecoveryCapability {
	return CrashRecoveryCapability{
		AdapterManifestHash: digest("test-adapter-manifest"),
		Mode:                CrashRecoveryProviderCorrelation, PerHeadInvocationKey: true,
		ProviderPersistedCorrelation: true, UniqueCorrelation: true, RecoveryByCorrelation: true,
	}
}

func (e *freshAbsenceExecutor) ExecuteCrashRecoverableReadOnly(ctx context.Context, invocation AdapterInvocation) providerexecutor.ExecutionResult {
	return e.execute(ctx, invocation.Request)
}

func (e *freshAbsenceExecutor) ExecuteCrashRecoverableMutation(ctx context.Context, invocation AdapterInvocation) providerexecutor.ExecutionResult {
	return e.execute(ctx, invocation.Request)
}

func (e *freshAbsenceExecutor) execute(ctx context.Context, request providerexecutor.ExecutionRequest) providerexecutor.ExecutionResult {
	if err := ctx.Err(); err != nil {
		return providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusFailed, Phase: providerexecutor.PhaseFailed,
			Reason: &providerexecutor.Reason{Code: providerexecutor.ReasonCodeProviderTimeout, Retryable: true},
		}
	}
	e.calls++
	switch e.calls {
	case 1:
		return providerexecutor.ExecutionResult{Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseDeleteAccepted}
	case 2:
		return providerexecutor.ExecutionResult{Status: providerexecutor.StatusPending, Phase: providerexecutor.PhaseAbsencePending}
	default:
		target := request.Command.Targets[0]
		evidence := providerexecutor.Evidence{
			Ref:    "provider-evidence://ionos/operations/delete-fresh",
			Digest: digest("absence-evidence"), Source: providerexecutor.EvidenceSourceProviderAPI,
			OperationID: request.Command.OperationID, LeaseRevision: request.Command.LeaseRevision,
			RuntimeServerID: request.Command.RuntimeServerID, ProviderID: request.Command.ProviderID,
			CapabilitySnapshotHash: request.Command.CapabilitySnapshotHash,
			BindingID:              target.BindingID, NativeRefHash: providerexecutor.ComputeNativeRefHash(target.NativeRef),
			ConnectionHash:       request.Command.ConnectionHash,
			ExecutionProfileHash: request.Command.ExecutionProfileHash,
			ResourceGraphHash:    request.Command.ResourceGraphHash,
			SubjectHash:          providerexecutor.ComputeEvidenceSubjectHash(request.Command, target, providerexecutor.ObservationAbsent),
			Observation:          providerexecutor.ObservationAbsent, Definitive: true,
			AttestationRef:    "provider-attestation://ionos/operations/delete-fresh",
			AttestationDigest: digest("absence-attestation"),
			CollectedAt:       request.Previous.PhaseEnteredAt.Add(time.Millisecond),
		}
		return providerexecutor.ExecutionResult{
			Status: providerexecutor.StatusSucceeded, Phase: providerexecutor.PhaseAbsent,
			Resources: []providerexecutor.ResourceBinding{{
				BindingID: target.BindingID, Kind: target.Kind, NativeRef: target.NativeRef,
				ParentBindingID: target.ParentBindingID, OwnershipHash: target.OwnershipHash,
				Disposition: target.Disposition, Observation: providerexecutor.ObservationAbsent,
				Cleanup: providerexecutor.CleanupComplete, Evidence: []providerexecutor.Evidence{evidence},
			}},
		}
	}
}
